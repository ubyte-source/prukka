// Package audio serves live dubbed output: one track-backed mixer template
// per session and language, with an independent cursor for every consumer.
package audio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/wasapi"
)

// chunk paces the encoder feed: 100 ms of reference audio per tick.
const chunk = 100 * time.Millisecond

// chunkSamples is the per-tick sample count.
const chunkSamples = int(pipeline.SampleRate * chunk / time.Second)

// aacArgs is the encoder setting every job shares. Package-level
// immutable data.
var aacArgs = []string{"-c:a", "aac", "-b:a", "128k"}

// VideoSource locates a session's video rendition and cue overlay for AV
// pushes; a miss keeps pushes audio-only.
type VideoSource interface {
	VideoPlaylist(slug string) (string, bool)
	CueFile(slug, lang string) (string, bool)
}

// noVideo is the default when no video source is wired.
type noVideo struct{}

func (noVideo) VideoPlaylist(string) (string, bool) { return "", false }
func (noVideo) CueFile(string, string) (string, bool) {
	return "", false
}

// Registry tracks live mixers and owns the long-lived encoder jobs; safe
// for concurrent use.
type Registry struct {
	// Wiring, immutable after construction.
	base  context.Context
	video VideoSource
	log   *slog.Logger
	sup   atomic.Pointer[ffmpeg.Supervisor]

	// Live state, guarded by mu.
	mixers  map[string]*pipeline.Mixer
	jobs    map[string]job
	gates   map[string]gate
	streams map[uint64]stream
	nextJob uint64
	nextOut uint64
	mu      sync.RWMutex
}

// job is one running encoder; the generation keeps a dead job from
// deregistering its replacement.
type job struct {
	cancel context.CancelFunc
	done   chan struct{}
	gen    uint64
	audio  bool
}

// gate is one session's lifetime: streams derive from ctx so Drop ends
// every listener.
type gate struct {
	ctx       context.Context
	cancel    context.CancelFunc
	finishing bool
}

// stream is one request-scoped MPEG-TS consumer. It participates in graceful
// finite playout but remains cancelable through the session gate.
type stream struct {
	done    chan struct{}
	session string
}

// NewRegistry wires the registry on the daemon-lifetime context; nil sup
// or video degrade to unavailable streaming or audio-only pushes.
func NewRegistry(base context.Context, sup *ffmpeg.Supervisor, video VideoSource, log *slog.Logger) *Registry {
	if video == nil {
		video = noVideo{}
	}

	registry := &Registry{
		base:    base,
		video:   video,
		log:     log,
		mixers:  map[string]*pipeline.Mixer{},
		jobs:    map[string]job{},
		gates:   map[string]gate{},
		streams: map[uint64]stream{},
	}
	registry.sup.Store(sup)

	return registry
}

// SetSupervisor makes a newly installed ffmpeg available to future jobs.
// Nil never removes a working supervisor.
func (r *Registry) SetSupervisor(sup *ffmpeg.Supervisor) {
	if sup != nil {
		r.sup.Store(sup)
	}
}

// key mirrors the vtt registry's session/lang scheme.
func key(session, lang string) string {
	return session + "/" + lang
}

func encoderJobKey(kind, session, lang, target string) string {
	id := kind + ":" + key(session, lang)
	if target == "" {
		return id
	}

	sum := sha256.Sum256([]byte(target))

	return fmt.Sprintf("%s:%x", id, sum[:8])
}

const maxPushTargetsPerPair = 8

func (r *Registry) jobIDLocked(kind, session, lang, target string) (string, error) {
	id := encoderJobKey(kind, session, lang, target)
	if kind != "push" {
		return id, nil
	}
	if _, exists := r.jobs[id]; exists {
		return id, nil
	}

	prefix := kind + ":" + key(session, lang) + ":"
	count := 0
	for existing := range r.jobs {
		if strings.HasPrefix(existing, prefix) {
			count++
		}
	}
	if count >= maxPushTargetsPerPair {
		return "", fmt.Errorf("push target limit reached for %s/%s (%d)", session, lang, maxPushTargetsPerPair)
	}

	return id, nil
}

// Create registers the mixer for one session and language.
func (r *Registry) Create(session string, lang core.Lang, m *pipeline.Mixer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gates[session]; !ok {
		ctx, cancel := context.WithCancel(r.base)
		r.gates[session] = gate{ctx: ctx, cancel: cancel}
	}

	r.mixers[key(session, string(lang))] = m
}

// WaitPlayout closes admission for a finite session, then waits for every
// started cursor and audio encoder to consume EOF and finish its sink. It never
// cancels jobs; ctx is the bounded failure path and Drop performs cancellation.
func (r *Registry) WaitPlayout(ctx context.Context, session string) error {
	mixers, jobs, streams, ok := r.finishSnapshot(session)
	if !ok {
		return nil
	}

	group, waitCtx := errgroup.WithContext(ctx)
	for _, mixer := range mixers {
		group.Go(func() error {
			return mixer.WaitPlayout(waitCtx)
		})
	}
	for _, done := range jobs {
		group.Go(func() error { return waitDone(waitCtx, done) })
	}
	for _, done := range streams {
		group.Go(func() error { return waitDone(waitCtx, done) })
	}

	return group.Wait()
}

func (r *Registry) finishSnapshot(
	session string,
) (mixers []*pipeline.Mixer, jobs, streams []<-chan struct{}, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	g, ok := r.gates[session]
	if !ok {
		return nil, nil, nil, false
	}
	g.finishing = true
	r.gates[session] = g

	prefix := session + "/"
	for id, mixer := range r.mixers {
		if strings.HasPrefix(id, prefix) {
			mixers = append(mixers, mixer)
		}
	}
	for id, job := range r.jobs {
		if _, pair, cut := strings.Cut(id, ":"); cut && job.audio && strings.HasPrefix(pair, prefix) {
			jobs = append(jobs, job.done)
		}
	}
	for _, output := range r.streams {
		if output.session == session {
			streams = append(streams, output.done)
		}
	}

	return mixers, jobs, streams, true
}

func waitDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Drop removes every mixer of one session and stops its encoder jobs.
func (r *Registry) Drop(session string) {
	r.mu.Lock()

	prefix := session + "/"

	for k := range r.mixers {
		if strings.HasPrefix(k, prefix) {
			delete(r.mixers, k)
		}
	}

	// Job keys start with "kind:session/lang" and may add a target digest.
	wait := make([]<-chan struct{}, 0)
	for k, j := range r.jobs {
		if _, pair, ok := strings.Cut(k, ":"); ok && strings.HasPrefix(pair, prefix) {
			j.cancel()
			if j.done != nil {
				wait = append(wait, j.done)
			}
			delete(r.jobs, k)
		}
	}

	if g, ok := r.gates[session]; ok {
		g.cancel()
		delete(r.gates, session)
	}
	r.dropStreamsLocked(session)
	r.mu.Unlock()

	for _, done := range wait {
		<-done
	}
}

func (r *Registry) dropStreamsLocked(session string) {
	for id, output := range r.streams {
		if output.session == session {
			delete(r.streams, id)
		}
	}
}

// Push streams one language's output to an RTMP or device:// target; jobs
// outlive the RPC, and restarting the same target replaces only that job.
func (r *Registry) Push(session, lang, target, subs string) error {
	if ffmpeg.IsDeviceURL(target) {
		return r.pushDevice(session, lang, target, subs)
	}
	format, err := networkMux(target)
	if err != nil {
		return err
	}

	audioArgs := append(append([]string{}, aacArgs...), "-f", format, target)

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return r.startJob("push", session, lang, target, audioArgs)
	}

	video := make([]string, 0, 8+len(aacArgs))
	video = append(video, "-c:v", "libx264", "-preset", "veryfast", "-b:v", "2500k")
	video = append(video, aacArgs...)
	video = append(video, "-f", format, target)

	return r.startAVJob("push", session, lang, target, playlist, r.burnFilter(session, lang, subs), video)
}

func networkMux(target string) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "", errors.New("push target is not a valid network URL")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "rtmp", "rtmps":
		return "flv", nil
	case "srt":
		return "mpegts", nil
	default:
		return "", fmt.Errorf("push target scheme %q: supported schemes are rtmp, rtmps, srt and device", parsed.Scheme)
	}
}

// pushDevice routes a push into a local device: audio takes the dub mix,
// video needs the session's video rendition.
func (r *Registry) pushDevice(session, lang, target, subs string) error {
	if ffmpeg.IsNativeVideoTarget(target) {
		if subs == "burn" {
			return fmt.Errorf("device target %q does not support burned subtitles yet", target)
		}

		playlist, hasVideo := r.video.VideoPlaylist(session)
		if !hasVideo {
			return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
		}

		return r.startVideoDeviceJob(session, lang, playlist, target)
	}

	if strings.HasPrefix(target, "device://audio/") && runtime.GOOS == "windows" {
		return r.launch("push", session, lang, target, func(context.Context) (io.WriteCloser, error) {
			return wasapi.Open(target)
		})
	}

	out, err := ffmpeg.DeviceOutputArgs(target)
	if err != nil {
		return err
	}

	if strings.HasPrefix(target, "device://audio/") {
		return r.startJob("push", session, lang, target, out)
	}

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
	}

	return r.startAVJob("push", session, lang, target, playlist,
		r.burnFilter(session, lang, subs), append([]string{"-an"}, out...))
}

func (r *Registry) startVideoDeviceJob(session, lang, playlist, target string) error {
	return r.launchProcess("push", session, lang, target, func(
		ctx context.Context, sup *ffmpeg.Supervisor,
	) (<-chan error, error) {
		return sup.StartVideoDevice(ctx, playlist, target)
	})
}

func (r *Registry) launchProcess(
	kind, session, lang, target string,
	start func(context.Context, *ffmpeg.Supervisor) (<-chan error, error),
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sup := r.sup.Load()
	if sup == nil {
		return fmt.Errorf("video output for %s/%s needs ffmpeg support", session, lang)
	}
	jobID, err := r.jobIDLocked(kind, session, lang, target)
	if err != nil {
		return err
	}
	if g, exists := r.gates[session]; !exists || g.finishing {
		return fmt.Errorf("%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	r.stopJobLocked(jobID)

	jobCtx, cancel := context.WithCancel(r.base)
	done, err := start(jobCtx, sup)
	if err != nil {
		cancel()

		return err
	}

	gen, jobDone := r.registerJobLocked(jobID, cancel, false)

	go func() {
		if waitErr := <-done; waitErr != nil && jobCtx.Err() == nil {
			r.log.Warn("native video job ended", "job", jobID, "err", waitErr)
		}
		close(jobDone)

		r.retireJob(jobID, gen, cancel)
	}()

	r.log.Info("native video job started", "job", jobID)

	return nil
}

// burnFilter builds the subs=burn overlay filter, or "" when impossible —
// a logged downgrade, never silent.
func (r *Registry) burnFilter(session, lang, subs string) string {
	if subs != "burn" {
		return ""
	}

	cueFile, ok := r.video.CueFile(session, lang)
	if !ok {
		r.log.Warn("burn-in unavailable: no live cue overlay", "session", session, "lang", lang)

		return ""
	}

	font := ffmpeg.DefaultFontFile()
	if font == "" {
		r.log.Warn("burn-in unavailable: no system font found", "session", session, "lang", lang)

		return ""
	}

	return ffmpeg.BurnFilter(cueFile, font)
}

// StartHLS encodes one language's mix as a rolling HLS rendition under
// dir, delay-shifted to align with the video; push lifecycle.
func (r *Registry) StartHLS(session, lang, dir string, delay time.Duration) error {
	return r.startJob("hls", session, lang, "", ffmpeg.HLSOutput(dir, delay, aacArgs...))
}

// startJob launches one long-lived audio-only encoder over a pair's mix.
func (r *Registry) startJob(kind, session, lang, target string, args []string) error {
	sup, err := r.requireSupervisor(session, lang)
	if err != nil {
		return err
	}

	return r.launch(kind, session, lang, target, func(ctx context.Context) (io.WriteCloser, error) {
		return sup.StartSink(ctx, args)
	})
}

// startAVJob launches one long-lived AV encoder: the session's live video
// rendition under the pair's mix, with an optional overlay filter.
func (r *Registry) startAVJob(kind, session, lang, target, playlist, vf string, output []string) error {
	sup, err := r.requireSupervisor(session, lang)
	if err != nil {
		return err
	}

	return r.launch(kind, session, lang, target, func(ctx context.Context) (io.WriteCloser, error) {
		return sup.StartAVSink(ctx, playlist, vf, output)
	})
}

func (r *Registry) requireSupervisor(session, lang string) (*ffmpeg.Supervisor, error) {
	sup := r.sup.Load()
	if sup == nil {
		return nil, fmt.Errorf("%w: no dubbed audio for %s/%s", core.ErrNotReady, session, lang)
	}

	return sup, nil
}

// launch runs one encoder job's shared lifecycle. The registry owns the
// job goroutine; cancel reaches it via the job context.
func (r *Registry) launch(
	kind, session, lang, target string, start func(context.Context) (io.WriteCloser, error),
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	template, ok := r.mixers[key(session, lang)]
	if !ok {
		return fmt.Errorf("%w: no dubbed audio for %s/%s", core.ErrNotReady, session, lang)
	}
	jobID, err := r.jobIDLocked(kind, session, lang, target)
	if err != nil {
		return err
	}
	if g, exists := r.gates[session]; !exists || g.finishing {
		return fmt.Errorf("%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	r.stopJobLocked(jobID)

	jobCtx, cancel := context.WithCancel(r.base)

	out, err := start(jobCtx)
	if err != nil {
		cancel()

		return err
	}

	// Admission and cursor registration happen under the same registry lock as
	// WaitPlayout's finishing transition. Even a sub-chunk finite source cannot
	// be sealed before its encoder consumes the first sample.
	fill := strings.HasPrefix(target, "device://audio/")
	cursor := template.Cursor()
	if fill {
		cursor = template.Live()
	}
	if !cursor.BeginPlayout() {
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}

	gen, jobDone := r.registerJobLocked(jobID, cancel, true)

	go func() {
		defer cursor.ReleasePlayout()

		// feed owns the writer's close — a second Close here would
		// re-drain and re-reap the encoder.
		if feedErr := feed(jobCtx, out, cursor, fill); feedErr != nil && jobCtx.Err() == nil {
			r.log.Warn("encoder job ended", "job", jobID, "err", feedErr)
		}
		close(jobDone)

		// A job that ended on its own deregisters itself — its own
		// generation only, never a replacement started meanwhile.
		r.retireJob(jobID, gen, cancel)
	}()

	r.log.Info("encoder job started", "job", jobID)

	return nil
}

func (r *Registry) stopJobLocked(jobID string) {
	old, exists := r.jobs[jobID]
	if !exists {
		return
	}

	old.cancel()
	if old.done != nil {
		<-old.done
	}
}

func (r *Registry) registerJobLocked(
	jobID string, cancel context.CancelFunc, consumesAudio bool,
) (gen uint64, done chan struct{}) {
	r.nextJob++
	done = make(chan struct{})
	r.jobs[jobID] = job{cancel: cancel, done: done, gen: r.nextJob, audio: consumesAudio}

	return r.nextJob, done
}

func (r *Registry) retireJob(jobID string, gen uint64, cancel context.CancelFunc) {
	r.mu.Lock()
	if current, exists := r.jobs[jobID]; exists && current.gen == gen {
		delete(r.jobs, jobID)
	}
	r.mu.Unlock()
	cancel()
}

// ServeTS encodes the pair's mix onto w until ctx ends, paced against real
// time; false means unknown pair or no ffmpeg.
func (r *Registry) ServeTS(ctx context.Context, w io.Writer, session, lang string) bool {
	sup := r.sup.Load()
	if sup == nil {
		return false
	}

	r.mu.Lock()
	template, ok := r.mixers[key(session, lang)]
	g, gateOK := r.gates[session]
	if !ok || !gateOK || g.finishing {
		r.mu.Unlock()

		return false
	}
	cursor := template.Cursor()
	if !cursor.BeginPlayout() {
		r.mu.Unlock()

		return false
	}
	r.nextOut++
	streamID := r.nextOut
	done := make(chan struct{})
	r.streams[streamID] = stream{session: session, done: done}
	r.mu.Unlock()

	defer func() {
		close(done)
		r.mu.Lock()
		delete(r.streams, streamID)
		r.mu.Unlock()
	}()

	// The stream runs under both the caller and the session's gate, so a
	// removed session ends its listeners.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(g.ctx, cancel)()

	if err := r.stream(streamCtx, w, cursor, sup); err != nil && streamCtx.Err() == nil {
		r.log.Warn("audio stream ended", "session", session, "lang", lang, "err", err)
	}

	return true
}

// stream runs one encoder: a feeder goroutine paces PCM into ffmpeg while
// the transport stream flows to the client.
func (r *Registry) stream(
	ctx context.Context, w io.Writer, mixer *pipeline.Mixer, sup *ffmpeg.Supervisor,
) error {
	defer mixer.ReleasePlayout()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	mux, err := sup.StartMux(streamCtx)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := mux.Close(); closeErr != nil {
			r.log.Debug("mux close", "err", closeErr)
		}
	}()

	feedDone := make(chan error, 1)
	go func() { feedDone <- feed(streamCtx, mux.In, mixer, false) }()

	_, copyErr := io.Copy(w, mux.Out)
	cancel()

	if feedErr := <-feedDone; feedErr != nil && copyErr == nil {
		return feedErr
	}

	if copyErr != nil {
		return fmt.Errorf("write transport stream: %w", copyErr)
	}

	return nil
}

// feed paces mixed PCM into the encoder until ctx ends, then closes the
// writer — feed is the writer's only owner.
func feed(ctx context.Context, in io.WriteCloser, mixer *pipeline.Mixer, fill bool) error {
	ticker := time.NewTicker(chunk)
	defer ticker.Stop()

	return feedTicks(ctx, in, mixer, fill, ticker.C)
}

func feedTicks(
	ctx context.Context, in io.WriteCloser, mixer *pipeline.Mixer, fill bool, ticks <-chan time.Time,
) error {
	err := paceTicks(ctx, in, mixer, fill, ticks)
	if closeErr := in.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close encoder feed: %w", closeErr)
	}
	mixer.ReleasePlayout()

	return err
}

// pace writes one real-time chunk per tick and stops when ctx ends. It
// waits for the mixer's anchor unless fill is set, where idle ticks carry
// silence instead — a starving device queue wedges and never recovers.
func pace(ctx context.Context, in io.Writer, mixer *pipeline.Mixer, fill bool) error {
	ticker := time.NewTicker(chunk)
	defer ticker.Stop()

	return paceTicks(ctx, in, mixer, fill, ticker.C)
}

func paceTicks(
	ctx context.Context, in io.Writer, mixer *pipeline.Mixer, fill bool, ticks <-chan time.Time,
) error {
	silence := make([]byte, chunkSamples*2)
	samples := make([]int16, chunkSamples)
	encoded := make([]byte, 0, chunkSamples*2)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}

		payload, ready := silence, fill
		pcm, status := mixer.NextInto(samples)
		if status == pipeline.PullEOF {
			return nil
		}
		if status == pipeline.PullReady {
			encoded = pipeline.AppendS16LE(encoded[:0], pcm.Data)
			payload, ready = encoded, true
		}
		if !ready {
			continue
		}

		if _, writeErr := in.Write(payload); writeErr != nil {
			return fmt.Errorf("feed encoder: %w", writeErr)
		}
	}
}

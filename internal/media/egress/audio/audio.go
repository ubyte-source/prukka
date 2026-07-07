// Package audio serves live dubbed output: one mixer per session and
// language, its clock advanced by exactly one consumer at a time.
package audio

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

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
	sup   *ffmpeg.Supervisor
	video VideoSource
	log   *slog.Logger

	// Live state, guarded by mu.
	mixers  map[string]*pipeline.Mixer
	jobs    map[string]job
	gates   map[string]gate
	nextJob uint64
	mu      sync.RWMutex
}

// job is one running encoder; the generation keeps a dead job from
// deregistering its replacement.
type job struct {
	cancel context.CancelFunc
	gen    uint64
}

// gate is one session's lifetime: streams derive from ctx so Drop ends
// every listener.
type gate struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRegistry wires the registry on the daemon-lifetime context; nil sup
// or video degrade to unavailable streaming or audio-only pushes.
func NewRegistry(base context.Context, sup *ffmpeg.Supervisor, video VideoSource, log *slog.Logger) *Registry {
	if video == nil {
		video = noVideo{}
	}

	return &Registry{
		base:   base,
		sup:    sup,
		video:  video,
		log:    log,
		mixers: map[string]*pipeline.Mixer{},
		jobs:   map[string]job{},
		gates:  map[string]gate{},
	}
}

// key mirrors the vtt registry's session/lang scheme.
func key(session, lang string) string {
	return session + "/" + lang
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

// Drop removes every mixer of one session and stops its encoder jobs.
func (r *Registry) Drop(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := session + "/"

	for k := range r.mixers {
		if strings.HasPrefix(k, prefix) {
			delete(r.mixers, k)
		}
	}

	// Job keys are "kind:session/lang".
	for k, j := range r.jobs {
		if _, pair, ok := strings.Cut(k, ":"); ok && strings.HasPrefix(pair, prefix) {
			j.cancel()
			delete(r.jobs, k)
		}
	}

	if g, ok := r.gates[session]; ok {
		g.cancel()
		delete(r.gates, session)
	}
}

// Push streams one language's output to an RTMP or device:// target; jobs
// outlive the RPC, and a second push for the same pair replaces the first.
func (r *Registry) Push(session, lang, target, subs string) error {
	if ffmpeg.IsDeviceURL(target) {
		return r.pushDevice(session, lang, target, subs)
	}

	audioArgs := append(append([]string{}, aacArgs...), "-f", "flv", target)

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return r.startJob("push", session, lang, audioArgs)
	}

	video := make([]string, 0, 8+len(aacArgs))
	video = append(video, "-c:v", "libx264", "-preset", "veryfast", "-b:v", "2500k")
	video = append(video, aacArgs...)
	video = append(video, "-f", "flv", target)

	return r.startAVJob("push", session, lang, playlist, r.burnFilter(session, lang, subs), video)
}

// pushDevice routes a push into a local device: audio takes the dub mix,
// video needs the session's video rendition.
func (r *Registry) pushDevice(session, lang, target, subs string) error {
	if strings.HasPrefix(target, "device://audio/") && runtime.GOOS == "windows" {
		return r.launch("push", session, lang, func(context.Context) (io.WriteCloser, error) {
			return wasapi.Open(target)
		})
	}

	out, err := ffmpeg.DeviceOutputArgs(target)
	if err != nil {
		return err
	}

	if strings.HasPrefix(target, "device://audio/") {
		return r.startJob("push", session, lang, out)
	}

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return fmt.Errorf("device target %q needs the session's video rendition (video source required)", target)
	}

	return r.startAVJob("push", session, lang, playlist,
		r.burnFilter(session, lang, subs), append([]string{"-an"}, out...))
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
	return r.startJob("hls", session, lang, ffmpeg.HLSOutput(dir, delay, aacArgs...))
}

// startJob launches one long-lived audio-only encoder over a pair's mix.
func (r *Registry) startJob(kind, session, lang string, args []string) error {
	return r.launch(kind, session, lang, func(ctx context.Context) (io.WriteCloser, error) {
		return r.sup.StartSink(ctx, args)
	})
}

// startAVJob launches one long-lived AV encoder: the session's live video
// rendition under the pair's mix, with an optional overlay filter.
func (r *Registry) startAVJob(kind, session, lang, playlist, vf string, output []string) error {
	return r.launch(kind, session, lang, func(ctx context.Context) (io.WriteCloser, error) {
		return r.sup.StartAVSink(ctx, playlist, vf, output)
	})
}

// launch runs one encoder job's shared lifecycle. The registry owns the
// job goroutine; cancel reaches it via the job context.
func (r *Registry) launch(
	kind, session, lang string, start func(context.Context) (io.WriteCloser, error),
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	mixer, ok := r.mixers[key(session, lang)]
	if !ok || r.sup == nil {
		return fmt.Errorf("no dubbed audio for %s/%s (is dubbing on and ffmpeg installed?)", session, lang)
	}

	jobCtx, cancel := context.WithCancel(r.base)

	out, err := start(jobCtx)
	if err != nil {
		cancel()

		return err
	}

	jobKey := kind + ":" + key(session, lang)
	if old, exists := r.jobs[jobKey]; exists {
		old.cancel()
	}

	r.nextJob++
	gen := r.nextJob
	r.jobs[jobKey] = job{cancel: cancel, gen: gen}

	go func() {
		// feed owns the writer's close — a second Close here would
		// re-drain and re-reap the encoder.
		if feedErr := feed(jobCtx, out, mixer); feedErr != nil && jobCtx.Err() == nil {
			r.log.Warn("encoder job ended", "job", jobKey, "err", feedErr)
		}

		// A job that ended on its own deregisters itself — its own
		// generation only, never a replacement started meanwhile.
		r.mu.Lock()
		if cur, exists := r.jobs[jobKey]; exists && cur.gen == gen {
			delete(r.jobs, jobKey)
		}
		r.mu.Unlock()

		cancel()
	}()

	r.log.Info("encoder job started", "job", jobKey)

	return nil
}

// ServeTS encodes the pair's mix onto w until ctx ends, paced against real
// time; false means unknown pair or no ffmpeg.
func (r *Registry) ServeTS(ctx context.Context, w io.Writer, session, lang string) bool {
	r.mu.RLock()
	mixer, ok := r.mixers[key(session, lang)]
	g := r.gates[session]
	r.mu.RUnlock()

	if !ok || r.sup == nil {
		return false
	}

	// The stream runs under both the caller and the session's gate, so a
	// removed session ends its listeners.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(g.ctx, cancel)()

	if err := r.stream(streamCtx, w, mixer); err != nil && streamCtx.Err() == nil {
		r.log.Warn("audio stream ended", "session", session, "lang", lang, "err", err)
	}

	return true
}

// stream runs one encoder: a feeder goroutine paces PCM into ffmpeg while
// the transport stream flows to the client.
func (r *Registry) stream(ctx context.Context, w io.Writer, mixer *pipeline.Mixer) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	mux, err := r.sup.StartMux(streamCtx)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := mux.Close(); closeErr != nil {
			r.log.Debug("mux close", "err", closeErr)
		}
	}()

	feedDone := make(chan error, 1)
	go func() { feedDone <- feed(streamCtx, mux.In, mixer) }()

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
func feed(ctx context.Context, in io.WriteCloser, mixer *pipeline.Mixer) error {
	err := pace(ctx, in, mixer)

	if closeErr := in.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close encoder feed: %w", closeErr)
	}

	return err
}

// pace writes one real-time chunk per tick; it waits for the mixer's
// anchor and stops when ctx ends.
func pace(ctx context.Context, in io.Writer, mixer *pipeline.Mixer) error {
	ticker := time.NewTicker(chunk)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		pcm, ready := mixer.Pull(chunkSamples)
		if !ready {
			continue
		}

		encoded, err := pipeline.EncodeS16LE(pcm.Data)
		if err != nil {
			return err
		}

		if _, writeErr := in.Write(encoded); writeErr != nil {
			return fmt.Errorf("feed encoder: %w", writeErr)
		}
	}
}

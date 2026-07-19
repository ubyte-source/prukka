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

// DefaultFeedQuantum is the amount of reference audio sent to an encoder or
// audio device on each pacing tick when a registration has no override.
const DefaultFeedQuantum = 100 * time.Millisecond

// windowsOS names the GOOS whose device pushes ride WASAPI (goconst).
const windowsOS = "windows"

// audibleTelemetryPeak ignores the first few integer samples of a fade edge.
// A peak at or above this level is roughly -42 dBFS: still conservative for
// speech, but strong enough to prove that meaningful PCM reached the sink.
const audibleTelemetryPeak = 256

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
	// playbackHelper resolves the native playback-helper binary for labeled
	// audio-device push targets; nil (or an empty result) keeps the ffmpeg
	// fallback. configStamp fingerprints a device output target for the
	// reconfiguration watcher; nil disables watching. outputIndexResolver
	// rebinds an output label to its current device index when the ffmpeg path
	// builds device args. The composition root wires all three through options
	// and none is mutated after construction.
	playbackHelper      func() string
	configStamp         func(target string) (string, bool)
	outputIndexResolver ffmpeg.OutputIndexResolver

	// Live state, guarded by mu.
	mixers  map[string]pipeline.Template
	feeds   map[string]feedConfig
	jobs    map[string]job
	gates   map[string]gate
	routes  map[string]pushRoute
	streams map[uint64]stream
	nextJob uint64
	nextOut uint64
	mu      sync.RWMutex

	// startMu serializes job (re)starts end-to-end: a replacement spans
	// several registry-lock windows (detach predecessor, spawn, admit), and
	// two concurrent starts for one job must not interleave. Control path —
	// Push-RPC cadence — so serializing starts is free.
	startMu sync.Mutex
}

// RegistrationOption configures one session/language mixer registration.
type RegistrationOption func(*feedConfig)

// WithFeedQuantum sets the PCM duration sent on each encoder or device pacing
// tick. The quantum must be positive and contain a whole number of
// reference-rate samples.
func WithFeedQuantum(quantum time.Duration) RegistrationOption {
	config := makeFeedConfig(quantum)

	return func(feed *feedConfig) { *feed = config }
}

type feedConfig struct {
	quantum time.Duration
	samples int
}

func makeFeedConfig(quantum time.Duration) feedConfig {
	return feedConfig{quantum: quantum, samples: pipeline.SamplesInQuantum(quantum)}
}

func defaultFeedConfig() feedConfig {
	return makeFeedConfig(DefaultFeedQuantum)
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

// Option configures a Registry at construction. Options carry the
// composition root's platform wiring so the registry holds no process-global
// mutable state.
type Option func(*Registry)

// WithPlaybackHelper wires the native playback-helper resolver used for
// labeled audio-device push targets. Without it, those targets fall back to
// the ffmpeg audiotoolbox path.
func WithPlaybackHelper(resolve func() string) Option {
	return func(r *Registry) { r.playbackHelper = resolve }
}

// WithConfigStamp wires the platform fingerprint for device output targets;
// the reconfiguration watcher forces a sink reopen when it changes. A nil
// stamp (the default) leaves device outputs unwatched.
func WithConfigStamp(stamp func(target string) (string, bool)) Option {
	return func(r *Registry) { r.configStamp = stamp }
}

// WithOutputIndexResolver wires the label-to-current-index lookup that rebinds
// an ffmpeg audio-device output to wherever its device sits now.
func WithOutputIndexResolver(resolve ffmpeg.OutputIndexResolver) Option {
	return func(r *Registry) { r.outputIndexResolver = resolve }
}

// NewRegistry wires the registry on the daemon-lifetime context; nil sup
// or video degrade to unavailable streaming or audio-only pushes. Options
// carry the platform device wiring.
func NewRegistry(
	base context.Context, sup *ffmpeg.Supervisor, video VideoSource, log *slog.Logger, opts ...Option,
) *Registry {
	if video == nil {
		video = noVideo{}
	}

	registry := &Registry{
		base:    base,
		video:   video,
		log:     log,
		mixers:  map[string]pipeline.Template{},
		feeds:   map[string]feedConfig{},
		jobs:    map[string]job{},
		routes:  map[string]pushRoute{},
		gates:   map[string]gate{},
		streams: map[uint64]stream{},
	}
	registry.sup.Store(sup)
	for _, opt := range opts {
		opt(registry)
	}

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

// The registry keys its live state by identity strings: a pair is
// "session/lang" and a job or route is "kind:session/lang[:targetdigest]".
// The helpers below own that grammar so no caller reparses it by hand — the
// separators live in exactly one place.

// pairOwnedBy reports whether a pair key belongs to session.
func pairOwnedBy(pairID, session string) bool {
	return strings.HasPrefix(pairID, session+"/")
}

// sessionOfPair returns the session that owns a pair key.
func sessionOfPair(pairID string) string {
	session, _, _ := strings.Cut(pairID, "/")

	return session
}

// jobOwnedBy reports whether a job or route key belongs to session, matching
// on the pair segment that follows the kind.
func jobOwnedBy(jobID, session string) bool {
	_, pair, ok := strings.Cut(jobID, ":")

	return ok && pairOwnedBy(pair, session)
}

// pushTargetPrefix is the key prefix every push target of one pair shares.
func pushTargetPrefix(pairID string) string {
	return "push:" + pairID + ":"
}

// sessionPushPrefix is the key prefix every push route of one session shares.
func sessionPushPrefix(session string) string {
	return "push:" + session + "/"
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

	prefix := pushTargetPrefix(key(session, lang))
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

// Create registers the mixer and feed pacing for one session and language.
func (r *Registry) Create(
	session string, lang core.Lang, m pipeline.Template, options ...RegistrationOption,
) {
	feed := defaultFeedConfig()
	for _, option := range options {
		option(&feed)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gates[session]; !ok {
		ctx, cancel := context.WithCancel(r.base)
		r.gates[session] = gate{ctx: ctx, cancel: cancel}
	}

	id := key(session, string(lang))
	r.mixers[id] = m
	r.feeds[id] = feed

	// A re-registered pair means a (re)started lane: replay this pair's
	// recorded push routes onto the fresh mixers, off the caller's path.
	go r.relaunchRoutes(id)
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
) (mixers []pipeline.Template, jobs, streams []<-chan struct{}, ok bool) {
	// Serialize with in-flight starts: see Reset.
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	g, ok := r.gates[session]
	if !ok {
		return nil, nil, nil, false
	}
	g.finishing = true
	r.gates[session] = g

	for id, mixer := range r.mixers {
		if pairOwnedBy(id, session) {
			mixers = append(mixers, mixer)
		}
	}
	for id, job := range r.jobs {
		if job.audio && jobOwnedBy(id, session) {
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

// Drop clears one session's live playout and forgets its push routes: the
// session is over. A restarting lane calls Reset instead.
func (r *Registry) Drop(session string) {
	r.Reset(session)

	r.mu.Lock()
	prefix := sessionPushPrefix(session)
	for id := range r.routes {
		if strings.HasPrefix(id, prefix) {
			delete(r.routes, id)
		}
	}
	r.mu.Unlock()
}

// Reset removes every mixer of one session and stops its encoder jobs while
// keeping the session's push routes: a restarted lane re-creates the pairs
// and the routes relaunch onto them. Taking startMu first lets any in-flight
// launch finish registering (or self-kill against the finishing gate) — a
// start between its two lock phases owns a live process the job table cannot
// see yet, and a teardown that ran past it would leak that process.
func (r *Registry) Reset(session string) {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()

	for k := range r.mixers {
		if pairOwnedBy(k, session) {
			delete(r.mixers, k)
			delete(r.feeds, k)
		}
	}

	wait := make([]<-chan struct{}, 0)
	for k, j := range r.jobs {
		if jobOwnedBy(k, session) {
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
// pushRoute is a user-requested output route. Routes are session intents:
// they outlive the lane's playout tree, which a failed lane drops and a
// restarted lane rebuilds, and they relaunch when their pair re-registers.
type pushRoute struct {
	session string
	lang    string
	target  string
	subs    string
}

// rememberRouteLocked records one push intent within the pair's target
// bound; re-pushing the same target refreshes the existing intent.
func (r *Registry) rememberRouteLocked(session, lang, target, subs string) {
	id := encoderJobKey("push", session, lang, target)
	if _, exists := r.routes[id]; !exists {
		prefix := pushTargetPrefix(key(session, lang))
		count := 0
		for existing := range r.routes {
			if strings.HasPrefix(existing, prefix) {
				count++
			}
		}
		if count >= maxPushTargetsPerPair {
			return
		}
	}
	r.routes[id] = pushRoute{session: session, lang: lang, target: target, subs: subs}
	r.log.Debug("push route remembered", "session", session, "lang", lang, "routes", len(r.routes))
}

// relaunchRoutes replays the recorded push intents of one pair onto its
// freshly registered mixers. Failures keep the intent for the next rebuild.
func (r *Registry) relaunchRoutes(pairID string) {
	r.mu.RLock()
	prefix := pushTargetPrefix(pairID)
	pending := make([]pushRoute, 0, 2)
	for id, route := range r.routes {
		if strings.HasPrefix(id, prefix) {
			pending = append(pending, route)
		}
	}
	r.mu.RUnlock()

	r.log.Debug("replaying push routes", "pair", pairID, "count", len(pending))
	for _, route := range pending {
		if err := r.push(route.session, route.lang, route.target, route.subs, false); err != nil {
			r.log.Warn("push route relaunch failed; kept for the next rebuild",
				"session", route.session, "lang", route.lang, "err", err)
		} else {
			r.log.Info("push route relaunched", "session", route.session, "lang", route.lang)
		}
	}
}

// Push starts one user-requested output route. Healthy and merely-not-ready
// outcomes are remembered as session intents so a rebuilt lane relaunches
// them; a target the daemon can never serve is not.
func (r *Registry) Push(session, lang, target, subs string) error {
	return r.push(session, lang, target, subs, true)
}

// push runs one route start; remember records not-ready intents. The route
// REPLAY path passes remember=false: replaying an intent must not resurrect
// it after a concurrent Drop already deleted the session — the intent either
// still exists (Drop not run) or must stay dead.
func (r *Registry) push(session, lang, target, subs string, remember bool) error {
	err := r.dispatchPush(session, lang, target, subs)
	if remember && (err == nil || errors.Is(err, core.ErrNotReady)) {
		r.mu.Lock()
		r.rememberRouteLocked(session, lang, target, subs)
		r.mu.Unlock()
	}

	return err
}

func (r *Registry) dispatchPush(session, lang, target, subs string) error {
	if ffmpeg.IsDeviceURL(target) {
		return r.pushDevice(session, lang, target, subs)
	}
	format, err := networkMux(target)
	if err != nil {
		return err
	}

	audioArgs := append(append([]string{}, aacArgs...), ffmpeg.OutputArgs(format, target)...)

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return r.startJob("push", session, lang, target, audioArgs)
	}

	video := make([]string, 0, 8+len(aacArgs))
	video = append(video, "-c:v", "libx264", "-preset", "veryfast", "-b:v", "2500k")
	video = append(video, aacArgs...)
	video = append(video, ffmpeg.OutputArgs(format, target)...)

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

	pacing := r.pacingFor(session, lang)
	bufferDuration := deviceBufferDuration(pacing)
	if ffmpeg.IsAudioDeviceTarget(target) && runtime.GOOS == windowsOS {
		return r.launch("push", session, lang, target, func(context.Context) (io.WriteCloser, error) {
			return wasapi.Open(target, wasapi.WithBufferDuration(bufferDuration))
		})
	}

	if ffmpeg.IsAudioDeviceTarget(target) {
		return r.startDeviceAudioJob(session, lang, target)
	}

	out, err := ffmpeg.DeviceOutputArgs(target, r.outputIndexResolver)
	if err != nil {
		return err
	}

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
	}

	return r.startAVJob("push", session, lang, target, playlist,
		r.burnFilter(session, lang, subs), append([]string{"-an"}, out...))
}

// deviceBufferDuration keeps two feed quanta queued in the platform playback
// layer. Calls therefore request 40 ms while the 100 ms broadcast feed retains
// the existing 200 ms WASAPI behavior.
func deviceBufferDuration(pacing feedConfig) time.Duration {
	return 2 * pacing.quantum
}

func (r *Registry) startVideoDeviceJob(session, lang, playlist, target string) error {
	return r.launchProcess("push", session, lang, target, func(
		ctx context.Context, sup *ffmpeg.Supervisor,
	) (<-chan error, error) {
		return sup.StartVideoDevice(ctx, playlist, target)
	})
}

// beginStart validates the session gate, derives the job ID and detaches the
// job's predecessor under the registry lock. The caller reaps the predecessor
// with reapJob OUTSIDE the lock: teardown can take seconds, and holding r.mu
// through it forced the pair-snapshot path into TryRLock polling.
func (r *Registry) beginStart(
	kind, session, lang, target string, needMixer bool,
) (jobID string, old job, had bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if needMixer {
		if _, ok := r.mixers[key(session, lang)]; !ok {
			return "", job{}, false, fmt.Errorf(
				"%w: no dubbed audio for %s/%s", core.ErrNotReady, session, lang)
		}
	}
	jobID, err = r.jobIDLocked(kind, session, lang, target)
	if err != nil {
		return "", job{}, false, err
	}
	if g, exists := r.gates[session]; !exists || g.finishing {
		return "", job{}, false, fmt.Errorf(
			"%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	old, had = r.jobs[jobID]
	delete(r.jobs, jobID)

	return jobID, old, had, nil
}

// reapJob cancels a detached predecessor and waits for its teardown.
func reapJob(old job, had bool) {
	if !had {
		return
	}
	old.cancel()
	if old.done != nil {
		<-old.done
	}
}

func (r *Registry) launchProcess(
	kind, session, lang, target string,
	start func(context.Context, *ffmpeg.Supervisor) (<-chan error, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	sup := r.sup.Load()
	if sup == nil {
		return fmt.Errorf("%w: ffmpeg is unavailable for %s/%s", core.ErrNotReady, session, lang)
	}

	jobID, old, had, err := r.beginStart(kind, session, lang, target, false)
	if err != nil {
		return err
	}
	reapJob(old, had)

	jobCtx, cancel := context.WithCancel(r.base)
	done, err := start(jobCtx, sup)
	if err != nil {
		cancel()

		return err
	}

	// Phase 2 — register under the lock, honoring a finishing transition that
	// may have landed while unlocked.
	r.mu.Lock()
	if g, exists := r.gates[session]; !exists || g.finishing {
		r.mu.Unlock()
		cancel()

		return fmt.Errorf("%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	gen, jobDone := r.registerJobLocked(jobID, cancel, false)
	r.mu.Unlock()

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

// sinkStarter starts one encoder process over prepared arguments; the ffmpeg
// supervisor satisfies it, and tests substitute a recorder.
type sinkStarter interface {
	StartSink(ctx context.Context, args []string) (io.WriteCloser, error)
}

func (r *Registry) resolvePlaybackHelper() string {
	if r.playbackHelper == nil {
		return ""
	}

	return r.playbackHelper()
}

// startDeviceAudioJob launches the audio-device push. A labeled target with
// the native helper available renders through the helper, which binds the
// output device by NAME — immune to the array reshuffling that Continuity
// devices cause. Otherwise the ffmpeg path applies, with its arguments
// rebuilt by the start hook on every (re)open so a reopen rebinds the label
// to the device's current index rather than injecting into whatever now sits
// at the stale position.
func (r *Registry) startDeviceAudioJob(session, lang, target string) error {
	if label := ffmpeg.DeviceTargetLabel(target); label != "" {
		if helper := r.resolvePlaybackHelper(); helper != "" {
			return r.launch("push", session, lang, target,
				func(ctx context.Context) (io.WriteCloser, error) {
					return ffmpeg.StartDevicePlayback(ctx, helper, label, core.SampleRate, r.log)
				})
		}
	}

	sup, err := r.requireSupervisor(session, lang)
	if err != nil {
		return err
	}
	// Reject a malformed target at push time, not at first reopen.
	if _, err := ffmpeg.DeviceOutputArgs(target, r.outputIndexResolver); err != nil {
		return err
	}

	return r.launch("push", session, lang, target, deviceAudioSinkStarter(sup, target, r.outputIndexResolver))
}

// deviceAudioSinkStarter returns the start hook for an audio-device push,
// resolving the device arguments fresh on every call.
func deviceAudioSinkStarter(
	sup sinkStarter, target string, resolve ffmpeg.OutputIndexResolver,
) func(context.Context) (io.WriteCloser, error) {
	return func(ctx context.Context) (io.WriteCloser, error) {
		args, err := ffmpeg.DeviceOutputArgs(target, resolve)
		if err != nil {
			return nil, err
		}

		return sup.StartSink(ctx, args)
	}
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
		return nil, fmt.Errorf("%w: ffmpeg is unavailable for %s/%s", core.ErrNotReady, session, lang)
	}

	return sup, nil
}

// launch runs one encoder job's shared lifecycle. The registry owns the
// job goroutine; cancel reaches it via the job context.
func (r *Registry) launch(
	kind, session, lang, target string, start func(context.Context) (io.WriteCloser, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	pairID := key(session, lang)
	jobID, old, had, err := r.beginStart(kind, session, lang, target, true)
	if err != nil {
		return err
	}
	reapJob(old, had)

	// Every device sink open — initial and reopen alike — carries the write
	// stall guard, so a wedged-but-alive encoder is severed and rebuilt
	// instead of blocking the feed forever.
	device := ffmpeg.IsAudioDeviceTarget(target)
	if device {
		start = guardedDeviceStart(start)
	}

	jobCtx, cancel := context.WithCancel(r.base)

	out, err := start(jobCtx)
	if err != nil {
		cancel()

		return err
	}

	// Phase 2 — admission and cursor registration under the same registry
	// lock as WaitPlayout's finishing transition, re-reading the pair state
	// that may have changed while unlocked. Even a sub-chunk finite source
	// cannot be sealed before its encoder consumes the first sample.
	r.mu.Lock()
	template, ok := r.mixers[pairID]
	g, exists := r.gates[session]
	if !ok || !exists || g.finishing {
		r.mu.Unlock()
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}
	pacing := r.pacingForLocked(pairID)
	cursor := template.Cursor()
	if !cursor.BeginPlayout() {
		r.mu.Unlock()
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}
	gen, jobDone := r.registerJobLocked(jobID, cancel, true)
	r.mu.Unlock()

	go func() {
		r.runEncoderJob(jobCtx, jobID, pairID, target, out,
			encoderBinding{template: template, cursor: cursor, pacing: pacing}, device, start)
		close(jobDone)

		// A job that ended on its own deregisters itself — its own
		// generation only, never a replacement started meanwhile.
		r.retireJob(jobID, gen, cancel)
	}()

	r.log.Info("encoder job started", "job", jobID)

	return nil
}

// encoderBinding is the mutable template/cursor/pacing one encoder job rides;
// a pair rebuild swaps all three atomically.
type encoderBinding struct {
	template pipeline.Template
	cursor   pipeline.Playout
	pacing   feedConfig
}

// runEncoderJob feeds one encoder until the job context ends, the session
// finishes or the feed fails. feed owns each writer's close — a second Close
// would re-drain and re-reap the encoder. Two recoveries keep long-lived
// outputs alive: a device reconfiguration reopens the sink on the same job
// and cursor, and a rebuilt pair (a restarted lane replaces its mixers, so
// the old cursor drains to EOF) re-attaches the job to the new template
// instead of silently retiring while the session still runs. device marks an
// audio-device sink: it silence-fills idle ticks, rides the stall guard, and
// self-heals through reopenDeviceSink instead of failing.
func (r *Registry) runEncoderJob(
	ctx context.Context, jobID, pairID, target string, writer io.WriteCloser,
	bind encoderBinding, device bool,
	start func(context.Context) (io.WriteCloser, error),
) {
	defer func() { bind.cursor.ReleasePlayout() }()
	audible := false
	observe := func(pcm core.PCM) {
		if audible {
			return
		}
		peak := pipeline.PeakS16(pcm.Data)
		if peak < audibleTelemetryPeak {
			return
		}

		audible = true
		r.log.Info("encoder received audible PCM",
			"job", jobID, "peak_s16", peak, "pts_ms", pcm.PTS.Milliseconds())
	}

	for {
		feedErr := r.feedWatched(ctx, writer, bind.cursor, device, bind.pacing, target, observe)
		next, verdict := r.recoverEncoder(ctx, feedErr, jobID, pairID, device, start, &bind)
		if verdict == encoderResume {
			writer = next

			continue
		}
		if verdict == encoderFailed && ctx.Err() == nil {
			r.log.Warn("encoder job ended", "job", jobID, "err", feedErr)
		}

		return
	}
}

// encoderVerdict is recoverEncoder's decision about a returned feed.
type encoderVerdict uint8

const (
	encoderResume encoderVerdict = iota
	encoderDone
	encoderFailed
)

// recoverEncoder decides how one feed return concludes: resume on a rebuilt
// pair, self-healing reopen for device sinks, quiet end when the session
// finishes, and failure otherwise. Device sinks (fill) retry with backoff on
// ANY feed error while the job lives: the platform device array flaps
// (Continuity devices come and go, OBS switches sample rates on attach), so a
// sink death there is environmental, not terminal — a route that silently
// died on the first hiccup left calls mute until a manual re-push.
func (r *Registry) recoverEncoder(
	ctx context.Context, feedErr error, jobID, pairID string, device bool,
	start func(context.Context) (io.WriteCloser, error), bind *encoderBinding,
) (io.WriteCloser, encoderVerdict) {
	if ctx.Err() != nil {
		return nil, encoderDone
	}
	if feedErr == nil {
		if !r.reattachEncoder(ctx, jobID, pairID, bind) {
			return nil, encoderDone // the session is finishing or gone
		}
		if device {
			return r.reopenDeviceSink(ctx, jobID, pairID, nil, start)
		}
		next, startErr := start(ctx)
		if startErr != nil {
			return nil, encoderFailed
		}

		return next, encoderResume
	}
	if device {
		return r.reopenDeviceSink(ctx, jobID, pairID, feedErr, start)
	}

	return nil, encoderFailed
}

// reattachEncoder waits for the pair's template to be rebuilt and moves the
// job onto it. It reports false when the session is finishing, dropped or
// the context ends — the job's ordinary conclusion.
func (r *Registry) reattachEncoder(
	ctx context.Context, jobID, pairID string, bind *encoderBinding,
) bool {
	ticker := time.NewTicker(pairRebuildPoll)
	defer ticker.Stop()

	session := sessionOfPair(pairID)
	for {
		next, nextPacing, state := r.pairSnapshot(pairID, session)
		switch {
		case state == pairGone:
			return false
		case state == pairCurrent && next != bind.template:
			fresh := next.Cursor()
			if !fresh.BeginPlayout() {
				return false
			}
			bind.cursor.ReleasePlayout()
			bind.cursor, bind.template, bind.pacing = fresh, next, nextPacing
			r.log.Info("pair rebuilt; encoder re-attached", "job", jobID)

			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// pairState is pairSnapshot's classification of one pair's registry entry.
type pairState uint8

const (
	// pairBusy means the registry lock was unavailable; poll again.
	pairBusy pairState = iota
	// pairCurrent carries a valid template for a live, unfinished session.
	pairCurrent
	// pairGone means the session is finishing or dropped.
	pairGone
)

// pairSnapshot reads one pair's template without ever blocking: launch
// replaces jobs while HOLDING the write lock and waiting for the old job to
// end, so a blocking read from a job goroutine would deadlock the pair.
func (r *Registry) pairSnapshot(pairID, session string) (pipeline.Template, feedConfig, pairState) {
	if !r.mu.TryRLock() {
		return nil, feedConfig{}, pairBusy
	}
	next, exists := r.mixers[pairID]
	sessionGate, gateExists := r.gates[session]
	nextPacing := r.feeds[pairID]
	r.mu.RUnlock()

	if !exists || !gateExists || sessionGate.finishing {
		return nil, feedConfig{}, pairGone
	}

	return next, nextPacing, pairCurrent
}

func (r *Registry) pacingForLocked(pairID string) feedConfig {
	if config, ok := r.feeds[pairID]; ok {
		return config
	}

	return defaultFeedConfig()
}

func (r *Registry) pacingFor(session, lang string) feedConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.pacingForLocked(key(session, lang))
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
	pairID := key(session, lang)
	template, ok := r.mixers[pairID]
	g, gateOK := r.gates[session]
	if !ok || !gateOK || g.finishing {
		r.mu.Unlock()

		return false
	}
	pacing := r.pacingForLocked(pairID)
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

	if err := r.stream(streamCtx, w, cursor, sup, pacing); err != nil && streamCtx.Err() == nil {
		r.log.Warn("audio stream ended", "session", session, "lang", lang, "err", err)
	}

	return true
}

// stream runs one encoder: a feeder goroutine paces PCM into ffmpeg while
// the transport stream flows to the client.
func (r *Registry) stream(
	ctx context.Context, w io.Writer, mixer pipeline.Playout, sup *ffmpeg.Supervisor, pacing feedConfig,
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
	go func() { feedDone <- feed(streamCtx, mux.In, mixer, false, pacing) }()

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

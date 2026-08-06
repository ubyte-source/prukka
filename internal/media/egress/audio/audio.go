// Package audio serves live dubbed output: one playout template per session
// and language, with an independent cursor for every consumer. Broadcast
// registers a track-backed mixer, a call a bounded voice queue with no bed,
// no clock and no delay.
package audio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// pairID is one session's output in one language: the unit a lane registers
// and every mix-fed job draws its PCM from.
type pairID struct {
	slug string
	lang core.Lang
}

func (p pairID) String() string {
	return p.slug + "/" + string(p.lang)
}

// jobKind separates the job families one pair can carry: the rolling HLS
// rendition the session owns, and the push targets a user asked for.
type jobKind string

const (
	kindHLS  jobKind = "hls"
	kindPush jobKind = "push"
)

// jobID is one encoder job — and, for a push, the route remembered under the
// same identity. HLS leaves the target empty: a pair has one rendition.
type jobID struct {
	kind   jobKind
	pair   pairID
	target string
}

// String renders a job for logs: its kind, its pair and a DIGEST of its
// target, because a push URL carries the stream key that authorizes it.
func (j jobID) String() string {
	if j.target == "" {
		return string(j.kind) + ":" + j.pair.String()
	}

	sum := sha256.Sum256([]byte(j.target))

	return fmt.Sprintf("%s:%s:%x", j.kind, j.pair, sum[:8])
}

// DefaultFeedQuantum is the amount of reference audio sent to an encoder or
// audio device on each pacing tick when a registration has no override.
const DefaultFeedQuantum = 100 * time.Millisecond

// audibleTelemetryPeak is roughly -42 dBFS: above a fade edge's first few
// integer samples, still conservative for speech.
const audibleTelemetryPeak = 256

// aacArgs is the encoder setting every job shares: a fresh slice per call,
// because every caller appends its own output arguments onto it.
func aacArgs() []string {
	return []string{"-c:a", "aac", "-b:a", "128k"}
}

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

// Registry tracks live pairs — one playout template with its feed pacing per
// session and language — and owns the long-lived encoder jobs; safe for
// concurrent use.
//
// Lock order: startMu is the OUTER lock, taken before live's and never inside
// it, and a job goroutine must NEVER take startMu — that is what makes reapJob
// and Reset's drain, which wait on a job's done channel while HOLDING startMu,
// deadlock-free.
type Registry struct {
	// Wiring, immutable after construction.
	base  context.Context
	video VideoSource
	log   *slog.Logger
	sup   atomic.Pointer[ffmpeg.Supervisor]

	playbackHelper      func() string
	configStamp         func(target string) (string, bool)
	outputIndexResolver ffmpeg.OutputIndexResolver

	live liveState

	// startMu serializes job (re)starts end-to-end: a replacement spans several
	// write windows (detach predecessor, spawn, admit).
	startMu sync.Mutex

	timing deviceTiming
}

// liveState is the registry's mutable population under the mutex that guards
// it; routes remembers one accepted push per job identity.
type liveState struct {
	pairs   map[pairID]registration
	jobs    map[jobID]job
	gates   map[string]*gate
	routes  map[jobID]session.SubsMode
	streams map[uint64]stream
	nextJob uint64
	nextOut uint64
	// teardowns counts completed Drops. A push samples it before dispatching
	// and rememberPush compares it before recording: no lock can order those
	// two moments, because a Push RPC can be parked anywhere inside its
	// dispatch while the teardown runs.
	teardowns uint64
	mu        sync.RWMutex
}

func (s *liveState) start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pairs = map[pairID]registration{}
	s.jobs = map[jobID]job{}
	s.gates = map[string]*gate{}
	s.routes = map[jobID]session.SubsMode{}
	s.streams = map[uint64]stream{}
}

func (s *liveState) teardownCount() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.teardowns
}

// deviceTiming is how fast a registry chases a flapping audio device; a zero
// field means the package default.
type deviceTiming struct {
	watch time.Duration
	stall time.Duration
	retry time.Duration
}

// registration is one live pair: the playout template a session and language
// publish, and the pacing its feeds run at.
type registration struct {
	template pipeline.Template
	pacing   feedConfig
}

// jobFeed is where one job's PCM comes from, which is what decides whether
// finite playout waits for it.
type jobFeed uint8

const (
	// jobSelfFed reads its own source and rides no mixer cursor: it never ends
	// on its own, so finite playout must not wait for it.
	jobSelfFed jobFeed = iota
	// jobMixFed draws the pair's mix through a cursor and ends at EOF.
	jobMixFed
)

// job is one running encoder; the generation keeps a dead job from
// deregistering its replacement.
type job struct {
	cancel context.CancelFunc
	done   chan struct{}
	gen    uint64
	feed   jobFeed
}

// gate is one session's lifetime: streams derive from ctx so Drop ends every
// listener, and it owns the drain of Create's route replays. Reset waits only
// on a gate it has already retired from the map, so no Add can race that Wait.
type gate struct {
	ctx       context.Context
	cancel    context.CancelFunc
	replays   sync.WaitGroup
	finishing bool
}

// stream is one request-scoped MPEG-TS consumer. It participates in graceful
// finite playout but remains cancelable through the session gate.
type stream struct {
	done chan struct{}
	slug string
}

// openStream is everything one accepted ServeTS carries out of the lock.
type openStream struct {
	gate   *gate
	cursor pipeline.Playout
	done   chan struct{}
	pacing feedConfig
	id     uint64
}

// RegistrationOption configures one session and language's registration.
type RegistrationOption func(*feedConfig)

// WithFeedQuantum sets the PCM duration sent on each pacing tick; it must be
// positive and a whole number of reference-rate samples.
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

// Option configures a Registry at construction.
type Option func(*Registry)

// WithPlaybackHelper wires the native playback-helper resolver for labeled
// audio-device push targets; without it they fall back to ffmpeg.
func WithPlaybackHelper(resolve func() string) Option {
	return func(r *Registry) { r.playbackHelper = resolve }
}

// WithConfigStamp wires the device-output fingerprint whose change makes the
// reconfiguration watcher reopen a sink; nil leaves device outputs unwatched.
func WithConfigStamp(stamp func(target string) (string, bool)) Option {
	return func(r *Registry) { r.configStamp = stamp }
}

// WithOutputIndexResolver wires the label-to-current-index lookup that rebinds
// an ffmpeg audio-device output to wherever its device sits now.
func WithOutputIndexResolver(resolve ffmpeg.OutputIndexResolver) Option {
	return func(r *Registry) { r.outputIndexResolver = resolve }
}

// withDeviceTiming shrinks the device-recovery schedule for tests.
func withDeviceTiming(timing deviceTiming) Option {
	return func(r *Registry) { r.timing = timing }
}

// NewRegistry wires the registry on the daemon-lifetime context; nil sup or
// video degrade to unavailable streaming or audio-only pushes.
func NewRegistry(
	base context.Context, sup *ffmpeg.Supervisor, video VideoSource, log *slog.Logger, opts ...Option,
) *Registry {
	if video == nil {
		video = noVideo{}
	}

	registry := &Registry{base: base, video: video, log: log}
	registry.live.start()
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

// maxPushTargetsPerPair bounds how many push targets one pair admits.
const maxPushTargetsPerPair = 8

func pushLimitError(pair pairID) error {
	return fmt.Errorf("push target limit reached for %s (%d)", pair, maxPushTargetsPerPair)
}

// admitPushTargetLocked reports whether one more push target fits the pair's
// bound. The population is the UNION of live jobs and remembered routes: the
// two maps diverge by design — retireJob forgets a dead job while Reset keeps
// its route across a lane restart.
func (s *liveState) admitPushTargetLocked(id jobID) bool {
	_, running := s.jobs[id]
	_, routed := s.routes[id]
	if running || routed {
		return true
	}

	count := 0
	for existing := range s.jobs {
		if existing.kind == kindPush && existing.pair == id.pair {
			count++
		}
	}
	for existing := range s.routes {
		_, live := s.jobs[existing]
		if !live && existing.kind == kindPush && existing.pair == id.pair {
			count++
		}
	}

	return count < maxPushTargetsPerPair
}

// liveGateLocked answers session ADMISSION only: a job, a cursor or a stream
// may start solely for a gate that is present and NOT finishing. A lifecycle
// caller that must reach a finishing session indexes s.gates directly.
func (s *liveState) liveGateLocked(slug string) (*gate, bool) {
	g, ok := s.gates[slug]
	if !ok || g.finishing {
		return nil, false
	}

	return g, true
}

// Create registers the playout template and feed pacing for one session and
// language, and schedules a replay of the pair's recorded push routes: it may
// start encoder and device processes off the caller's path.
func (r *Registry) Create(
	slug string, lang core.Lang, template pipeline.Template, options ...RegistrationOption,
) {
	feed := defaultFeedConfig()
	for _, option := range options {
		option(&feed)
	}

	pair := pairID{slug: slug, lang: lang}
	g := r.live.createPair(r.base, pair, registration{template: template, pacing: feed})

	// A re-registered pair means a (re)started lane: replay its recorded push
	// routes onto the fresh template, off the caller's path.
	go func() {
		defer g.replays.Done()

		r.relaunchRoutes(g.ctx, pair)
	}()
}

// createPair publishes the pair and enrolls one replay in its gate's drain:
// Reset reads that gate in the same lock, so its Wait cannot miss the replay.
func (s *liveState) createPair(base context.Context, pair pairID, reg registration) *gate {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.gates[pair.slug]
	if !ok {
		ctx, cancel := context.WithCancel(base)
		g = &gate{ctx: ctx, cancel: cancel}
		s.gates[pair.slug] = g
	}
	s.pairs[pair] = reg
	g.replays.Add(1)

	return g
}

// WaitPlayout closes admission for a finite session, then waits for every
// started cursor and audio encoder to consume EOF and finish its sink. It never
// cancels jobs; ctx is the bounded failure path and Drop performs cancellation.
func (r *Registry) WaitPlayout(ctx context.Context, slug string) error {
	templates, jobs, streams, ok := r.finishSnapshot(slug)
	if !ok {
		return nil
	}

	group, waitCtx := errgroup.WithContext(ctx)
	for _, template := range templates {
		group.Go(func() error {
			return template.WaitPlayout(waitCtx)
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
	slug string,
) (templates []pipeline.Template, jobs, streams []<-chan struct{}, ok bool) {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	return r.live.finishSnapshot(slug)
}

func (s *liveState) finishSnapshot(
	slug string,
) (templates []pipeline.Template, jobs, streams []<-chan struct{}, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, present := s.gates[slug]
	if !present {
		return nil, nil, nil, false
	}
	g.finishing = true

	for pair, reg := range s.pairs {
		if pair.slug == slug {
			templates = append(templates, reg.template)
		}
	}
	for id, j := range s.jobs {
		if j.feed == jobMixFed && id.pair.slug == slug {
			jobs = append(jobs, j.done)
		}
	}
	for _, output := range s.streams {
		if output.slug == slug {
			streams = append(streams, output.done)
		}
	}

	return templates, jobs, streams, true
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
func (r *Registry) Drop(slug string) {
	r.Reset(slug)
	r.live.forgetRoutes(slug)
}

func (s *liveState) forgetRoutes(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id := range s.routes {
		if id.kind == kindPush && id.pair.slug == slug {
			delete(s.routes, id)
		}
	}
	s.teardowns++
}

// Reset removes every template of one session and stops its encoder jobs while
// keeping the session's push routes: a restarted lane re-creates the pairs and
// the routes relaunch onto them. startMu is taken first, or a launch parked
// between its two lock phases would leak the process it already started.
func (r *Registry) Reset(slug string) {
	// Retire the gate first, and drain BEFORE taking startMu: a replay launching
	// a push blocks on startMu, so waiting under it would deadlock.
	if g, retired := r.live.retireGate(slug); retired {
		g.replays.Wait()
	}

	r.startMu.Lock()
	defer r.startMu.Unlock()

	for _, done := range r.live.stopSession(slug) {
		<-done
	}
}

func (s *liveState) retireGate(slug string) (*gate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.gates[slug]
	if !ok {
		return nil, false
	}
	g.cancel()
	delete(s.gates, slug)

	return g, true
}

func (s *liveState) stopSession(slug string) []<-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	wait := make([]<-chan struct{}, 0)
	for pair := range s.pairs {
		if pair.slug == slug {
			delete(s.pairs, pair)
		}
	}
	for id, j := range s.jobs {
		if id.pair.slug == slug {
			j.cancel()
			if j.done != nil {
				wait = append(wait, j.done)
			}
			delete(s.jobs, id)
		}
	}

	// A Create between the two windows left a fresh gate whose replays run
	// against a session being torn down; cancel and drop it too.
	if late, present := s.gates[slug]; present {
		late.cancel()
		delete(s.gates, slug)
	}
	for id, output := range s.streams {
		if output.slug == slug {
			delete(s.streams, id)
		}
	}

	return wait
}

// pushRoute is one recorded route carried out of the registry lock: the job
// identity and the caption overlay the push asked for.
type pushRoute struct {
	id   jobID
	subs session.SubsMode
}

func (s *liveState) pushRoutes(pair pairID) []pushRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pending := make([]pushRoute, 0, 2)
	for id, subs := range s.routes {
		if id.kind == kindPush && id.pair == pair {
			pending = append(pending, pushRoute{id: id, subs: subs})
		}
	}

	return pending
}

// rememberPush records the intent behind an accepted push; teardowns is the
// count the caller read before dispatching. That count, not the missing gate,
// is the discriminator: a push that legitimately precedes the lane's first
// pair also finds no gate. A STARTED push already passed the pair's bound in
// beginStart and must never be refused here; a not-ready one is admitted in
// the same critical section that records it.
func (r *Registry) rememberPush(id jobID, subs session.SubsMode, started bool, teardowns uint64) error {
	pair := id.pair
	routes, recorded, err := r.live.rememberPush(id, subs, started, teardowns)
	switch {
	case err != nil:
		return err
	case recorded:
		r.log.Debug("push route remembered", "session", pair.slug, "lang", pair.lang, "routes", routes)
	default:
		r.log.Warn("push intent dropped: the session was torn down during this push",
			"session", pair.slug, "lang", pair.lang)
	}

	return nil
}

func (s *liveState) rememberPush(
	id jobID, subs session.SubsMode, started bool, teardowns uint64,
) (routes int, recorded bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, gated := s.gates[id.pair.slug]; !gated && teardowns != s.teardowns {
		return 0, false, nil
	}
	if !started && !s.admitPushTargetLocked(id) {
		return 0, false, pushLimitError(id.pair)
	}
	s.routes[id] = subs

	return len(s.routes), true, nil
}

// relaunchRoutes replays one pair's recorded push intents onto its freshly
// registered template; a failure keeps the intent for the next rebuild.
func (r *Registry) relaunchRoutes(ctx context.Context, pair pairID) {
	pending := r.live.pushRoutes(pair)

	r.log.Debug("replaying push routes", "pair", pair.String(), "count", len(pending))
	for _, route := range pending {
		if ctx.Err() != nil {
			return
		}
		if err := r.push(route.id, route.subs, pushReplayed); err != nil {
			r.log.Warn("push route relaunch failed; kept for the next rebuild",
				"session", pair.slug, "lang", pair.lang, "err", err)
		} else {
			r.log.Info("push route relaunched", "session", pair.slug, "lang", pair.lang)
		}
	}
}

// StartHLS encodes one language's mix as a rolling HLS rendition under dir,
// delay-shifted to align with the video.
func (r *Registry) StartHLS(slug string, lang core.Lang, dir string, delay time.Duration) error {
	id := jobID{kind: kindHLS, pair: pairID{slug: slug, lang: lang}}

	return r.startJob(id, ffmpeg.HLSOutput(dir, delay, aacArgs()...))
}

// startJob launches one long-lived audio-only encoder over a pair's mix.
func (r *Registry) startJob(id jobID, args []string) error {
	sup, err := r.requireSupervisor(id.pair)
	if err != nil {
		return err
	}

	return r.launchMixFedJob(id, func(ctx context.Context) (io.WriteCloser, error) {
		return sup.StartSink(ctx, args)
	})
}

// startAVJob launches one long-lived AV encoder: the session's live video
// rendition under the pair's mix, with an optional overlay filter.
func (r *Registry) startAVJob(id jobID, playlist, vf string, output []string) error {
	sup, err := r.requireSupervisor(id.pair)
	if err != nil {
		return err
	}

	return r.launchMixFedJob(id, func(ctx context.Context) (io.WriteCloser, error) {
		return sup.StartAVSink(ctx, playlist, vf, output)
	})
}

func (r *Registry) requireSupervisor(pair pairID) (*ffmpeg.Supervisor, error) {
	sup := r.sup.Load()
	if sup == nil {
		return nil, fmt.Errorf("%w: ffmpeg is unavailable for %s", core.ErrNotReady, pair)
	}

	return sup, nil
}

// beginStart validates the session gate, admits a push against the pair's
// bound and detaches the job's predecessor in one write window. The caller
// reaps the predecessor with reapJob OUTSIDE it: teardown can take seconds.
func (s *liveState) beginStart(id jobID, feed jobFeed) (old job, had bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pairs[id.pair]; feed == jobMixFed && !ok {
		return job{}, false, fmt.Errorf("%w: no dubbed audio for %s", core.ErrNotReady, id.pair)
	}
	if id.kind == kindPush && !s.admitPushTargetLocked(id) {
		return job{}, false, pushLimitError(id.pair)
	}
	if _, live := s.liveGateLocked(id.pair.slug); !live {
		return job{}, false, fmt.Errorf("%w: playout is finishing for %s", core.ErrNotReady, id.pair)
	}
	old, had = s.jobs[id]
	delete(s.jobs, id)

	return old, had, nil
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

// sinkKind is what an encoder job writes into: a platform playback queue
// wedges when a tick brings it nothing, gets reconfigured under the job and
// dies environmentally; a pipe to an encoder does none of that.
type sinkKind uint8

const (
	sinkEncoder sinkKind = iota
	sinkDevice
)

// fillsIdleTicks reports whether a tick that finds no PCM still writes a
// quantum of silence; an encoder instead stays anchored to the mixer.
func (k sinkKind) fillsIdleTicks() bool {
	return k == sinkDevice
}

// launchMixFedJob runs the lifecycle of a jobMixFed encoder: it owns a mixer
// cursor and pumps the pair's PCM into its sink.
func (r *Registry) launchMixFedJob(
	id jobID, start func(context.Context) (io.WriteCloser, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	old, had, err := r.live.beginStart(id, jobMixFed)
	if err != nil {
		return err
	}
	reapJob(old, had)

	sink := sinkEncoder
	if deviceurl.IsKind(id.target, deviceurl.Audio) {
		sink = sinkDevice
		start = guardedDeviceStart(start, r.deviceWriteStallTimeout())
	}

	jobCtx, cancel := context.WithCancel(r.base)

	out, err := start(jobCtx)
	if err != nil {
		cancel()

		return err
	}

	bind, admitted, ok := r.live.admitMixFedJob(id, cancel)
	if !ok {
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}

	go func() {
		r.runEncoderJob(jobCtx, id, out, bind, sink, start)
		close(admitted.done)

		r.retireJob(id, admitted.gen, cancel)
	}()

	r.log.Info("encoder job started", "job", id.String())

	return nil
}

// launchSelfFedJob runs the lifecycle of a jobSelfFed process: it reads its
// own source, rides no mixer cursor and takes no PCM from the pair.
func (r *Registry) launchSelfFedJob(
	id jobID, start func(context.Context, *ffmpeg.Supervisor) (<-chan error, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	sup := r.sup.Load()
	if sup == nil {
		return fmt.Errorf("%w: ffmpeg is unavailable for %s", core.ErrNotReady, id.pair)
	}

	old, had, err := r.live.beginStart(id, jobSelfFed)
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

	admitted, ok := r.live.admitSelfFedJob(id, cancel)
	if !ok {
		cancel()

		return fmt.Errorf("%w: playout is finishing for %s", core.ErrNotReady, id.pair)
	}

	label := id.String()
	go func() {
		if waitErr := <-done; waitErr != nil && jobCtx.Err() == nil {
			r.log.Warn("native video job ended", "job", label, "err", waitErr)
		}
		close(admitted.done)

		r.retireJob(id, admitted.gen, cancel)
	}()

	r.log.Info("native video job started", "job", label)

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
// finishes or the feed fails. feed owns each writer's close: a second Close
// would re-drain and re-reap the encoder.
func (r *Registry) runEncoderJob(
	ctx context.Context, id jobID, writer io.WriteCloser,
	bind encoderBinding, sink sinkKind,
	start func(context.Context) (io.WriteCloser, error),
) {
	defer func() { bind.cursor.ReleasePlayout() }()
	label := id.String()
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
			"job", label, "peak_s16", peak, "pts_ms", pcm.PTS.Milliseconds())
	}

	for {
		feedErr := r.feedWatched(ctx, &feeder{
			out:       writer,
			mixer:     bind.cursor,
			target:    id.target,
			observers: []func(core.PCM){observe},
			pacing:    bind.pacing,
			sink:      sink,
		})
		next, verdict := r.recoverEncoder(ctx, feedErr, id, sink, start, &bind)
		if verdict == encoderResume {
			writer = next

			continue
		}
		if verdict == encoderFailed && ctx.Err() == nil {
			r.log.Warn("encoder job ended", "job", label, "err", feedErr)
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
// pair, reopen for device sinks, quiet end when the session finishes, failure
// otherwise. A device sink retries with backoff on ANY feed error, because the
// platform device array flaps (Continuity devices come and go, OBS switches
// sample rates on attach) and a death there is environmental, not terminal.
func (r *Registry) recoverEncoder(
	ctx context.Context, feedErr error, id jobID, sink sinkKind,
	start func(context.Context) (io.WriteCloser, error), bind *encoderBinding,
) (io.WriteCloser, encoderVerdict) {
	if ctx.Err() != nil {
		return nil, encoderDone
	}
	if feedErr == nil {
		if !r.reattachEncoder(ctx, id, bind) {
			return nil, encoderDone // the session is finishing or gone
		}
		if sink == sinkDevice {
			return r.reopenDeviceSink(ctx, id, nil, start)
		}
		next, startErr := start(ctx)
		if startErr != nil {
			return nil, encoderFailed
		}

		return next, encoderResume
	}
	if sink == sinkDevice {
		return r.reopenDeviceSink(ctx, id, feedErr, start)
	}

	return nil, encoderFailed
}

// reattachEncoder waits for the pair's template to be rebuilt and moves the
// job onto it; false is the job's ordinary conclusion.
func (r *Registry) reattachEncoder(ctx context.Context, id jobID, bind *encoderBinding) bool {
	ticker := time.NewTicker(pairRebuildPoll)
	defer ticker.Stop()

	for {
		next, state := r.live.pairSnapshot(id.pair)
		switch {
		case state == pairGone:
			return false
		case state == pairCurrent && next.template != bind.template:
			fresh := next.template.Cursor()
			if !fresh.BeginPlayout() {
				return false
			}
			bind.cursor.ReleasePlayout()
			bind.cursor, bind.template, bind.pacing = fresh, next.template, next.pacing
			r.log.Info("pair rebuilt; encoder re-attached", "job", id.String())

			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// admitMixFedJob shares ONE window with finishSnapshot: even a sub-chunk
// finite source must not be sealed before its encoder takes the first sample.
func (s *liveState) admitMixFedJob(id jobID, cancel context.CancelFunc) (encoderBinding, job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, exists := s.pairs[id.pair]
	if _, live := s.liveGateLocked(id.pair.slug); !exists || !live {
		return encoderBinding{}, job{}, false
	}
	cursor := reg.template.Cursor()
	if !cursor.BeginPlayout() {
		return encoderBinding{}, job{}, false
	}
	bind := encoderBinding{template: reg.template, cursor: cursor, pacing: reg.pacing}

	return bind, s.registerJobLocked(id, cancel, jobMixFed), true
}

func (s *liveState) admitSelfFedJob(id jobID, cancel context.CancelFunc) (job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, live := s.liveGateLocked(id.pair.slug); !live {
		return job{}, false
	}

	return s.registerJobLocked(id, cancel, jobSelfFed), true
}

func (s *liveState) registerJobLocked(id jobID, cancel context.CancelFunc, feed jobFeed) job {
	s.nextJob++
	registered := job{cancel: cancel, done: make(chan struct{}), gen: s.nextJob, feed: feed}
	s.jobs[id] = registered

	return registered
}

func (r *Registry) retireJob(id jobID, gen uint64, cancel context.CancelFunc) {
	r.live.retireJob(id, gen)
	cancel()
}

func (s *liveState) retireJob(id jobID, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current, exists := s.jobs[id]; exists && current.gen == gen {
		delete(s.jobs, id)
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

// pairSnapshot reads one pair's registration without ever blocking: a job
// goroutine must stay responsive to its own cancellation while a reaper waits
// on its done channel.
func (s *liveState) pairSnapshot(pair pairID) (registration, pairState) {
	if !s.mu.TryRLock() {
		return registration{}, pairBusy
	}
	defer s.mu.RUnlock()

	reg, exists := s.pairs[pair]
	if _, live := s.liveGateLocked(pair.slug); !exists || !live {
		return registration{}, pairGone
	}

	return reg, pairCurrent
}

// pacingFor answers for a pair that may not be registered yet: pushDevice
// sizes the platform device buffer before any pair existence check.
func (s *liveState) pacingFor(pair pairID) feedConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if reg, ok := s.pairs[pair]; ok {
		return reg.pacing
	}

	return defaultFeedConfig()
}

// ServeTS encodes the pair's mix onto w until ctx ends, paced against real
// time; false means unknown pair or no ffmpeg.
func (r *Registry) ServeTS(ctx context.Context, w io.Writer, slug, lang string) bool {
	sup := r.sup.Load()
	if sup == nil {
		return false
	}

	out, ok := r.live.openStream(pairID{slug: slug, lang: core.Lang(lang)})
	if !ok {
		return false
	}

	defer func() {
		close(out.done)
		r.live.closeStream(out.id)
	}()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(out.gate.ctx, cancel)()

	if err := r.stream(streamCtx, w, out.cursor, sup, out.pacing); err != nil && streamCtx.Err() == nil {
		r.log.Warn("audio stream ended", "session", slug, "lang", lang, "err", err)
	}

	return true
}

func (s *liveState) openStream(pair pairID) (openStream, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg, exists := s.pairs[pair]
	sessionGate, live := s.liveGateLocked(pair.slug)
	if !exists || !live {
		return openStream{}, false
	}
	cursor := reg.template.Cursor()
	if !cursor.BeginPlayout() {
		return openStream{}, false
	}
	s.nextOut++
	out := openStream{
		gate:   sessionGate,
		cursor: cursor,
		done:   make(chan struct{}),
		pacing: reg.pacing,
		id:     s.nextOut,
	}
	s.streams[out.id] = stream{slug: pair.slug, done: out.done}

	return out, true
}

func (s *liveState) closeStream(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.streams, id)
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
	streamFeed := &feeder{out: mux.In, mixer: mixer, pacing: pacing, sink: sinkEncoder}
	go func() { feedDone <- streamFeed.run(streamCtx) }()

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

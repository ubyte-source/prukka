// Package audio serves live dubbed output: one playout template per session
// and language, with an independent cursor for every consumer. Broadcast
// registers a track-backed mixer — a delayed bed with the dubbed voice ducked
// over it; a call registers a bounded voice queue instead, a bare FIFO with
// no bed, no clock and no delay, because a live conversation cannot wait.
package audio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// DefaultFeedQuantum is the amount of reference audio sent to an encoder or
// audio device on each pacing tick when a registration has no override.
const DefaultFeedQuantum = 100 * time.Millisecond

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

// Registry tracks live pairs — one mixer template with its feed pacing per
// session and language — and owns the long-lived encoder jobs; safe for
// concurrent use.
//
// Locking. Two mutexes and three rules. Every acquisition of either lock
// lives in THIS file: push.go decides which target and which arguments and
// takes no lock, device.go and feed.go run inside a job goroutine and take
// none either — so the rules stay verifiable by reading audio.go alone.
//
// Order: startMu is the OUTER lock. It is acquired before mu and never while
// mu is held; the four sites are finishSnapshot, Reset, launchSelfFedJob and
// launchMixFedJob.
//
// Replay drain: no lock is held across g.replays.Wait(). A replay that
// launches a push blocks on startMu, so waiting under either lock deadlocks.
//
// Job goroutines: a job goroutine must NEVER acquire startMu — it takes mu
// alone (pairSnapshot's TryRLock, retireJob). That is what makes reapJob and
// Reset's drain, which both wait on a job's done channel while HOLDING
// startMu, deadlock-free. Routing recovery or a restart through a launcher
// would void it silently.
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
	pairs   map[string]registration
	jobs    map[string]job
	gates   map[string]*gate
	routes  map[string]pushRoute
	streams map[uint64]stream
	nextJob uint64
	nextOut uint64
	mu      sync.RWMutex

	// teardowns counts completed Drops. A push reads it before dispatching and
	// rememberPush re-reads it before recording, which is what keeps an intent
	// from outliving the session it names: no lock can order those two moments,
	// because a Push RPC can be parked anywhere inside its dispatch while the
	// whole teardown runs. Atomic so the push path pays no lock for the
	// snapshot; incremented inside Drop's mu window so it is published together
	// with the route sweep it guards.
	teardowns atomic.Uint64

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

// registration is one live pair: the mixer template a session and language
// publish, and the pacing its feeds run at. The two are ONE value because
// they are created, replaced and retired as a unit — a template whose pacing
// went missing would feed a zero quantum into time.NewTicker.
type registration struct {
	template pipeline.Template
	pacing   feedConfig
}

// job is one running encoder; the generation keeps a dead job from
// deregistering its replacement. consumesMix marks a job that draws the
// pair's mix and therefore ends at EOF: WaitPlayout waits on exactly those,
// so a self-feeding native process must stay false or finite playout never
// completes.
type job struct {
	cancel      context.CancelFunc
	done        chan struct{}
	gen         uint64
	consumesMix bool
}

// The two values of a job's consumesMix bit, named so the launch sites read
// as a decision instead of a bare true/false.
const (
	consumesPairMix = true
	selfFeeding     = false
)

// gate is one session's lifetime: streams derive from ctx so Drop ends
// every listener, and the gate owns the drain of the route replays Create
// schedules for its session. Adds happen under the registry lock while the
// gate is still published; Reset waits only on a gate it has already retired
// from the map, so an Add can never race that Wait.
type gate struct {
	ctx       context.Context
	cancel    context.CancelFunc
	replays   sync.WaitGroup
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
		pairs:   map[string]registration{},
		jobs:    map[string]job{},
		routes:  map[string]pushRoute{},
		gates:   map[string]*gate{},
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
// The helpers below own that grammar so no caller reparses it by hand.

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

func pushLimitError(session, lang string) error {
	return fmt.Errorf("push target limit reached for %s/%s (%d)", session, lang, maxPushTargetsPerPair)
}

// admitPushTargetLocked reports whether one more push target fits the pair's
// bound. A pair's population is the UNION of its live encoder jobs and its
// remembered routes: the two maps diverge by design — retireJob forgets a dead
// job while Reset keeps its route across a lane restart — so counting either
// half alone admits a target the other half already claims. A target the pair
// already holds is a refresh, never a new one.
func (r *Registry) admitPushTargetLocked(pairID, id string) bool {
	_, running := r.jobs[id]
	_, routed := r.routes[id]
	if running || routed {
		return true
	}

	prefix := pushTargetPrefix(pairID)
	count := 0
	for existing := range r.jobs {
		if strings.HasPrefix(existing, prefix) {
			count++
		}
	}
	for existing := range r.routes {
		if _, live := r.jobs[existing]; !live && strings.HasPrefix(existing, prefix) {
			count++
		}
	}

	return count < maxPushTargetsPerPair
}

// liveGateLocked is the single authority on session admission: a job, a
// cursor or a stream may start only for a session whose gate is present and
// NOT finishing. It answers nil unless both hold, so a caller cannot act on a
// gate it was just told to reject. Create, finishSnapshot and Reset read
// r.gates raw on purpose — they are lifecycle, not admission, and must still
// reach the gate of a session that is already finishing.
func (r *Registry) liveGateLocked(session string) (*gate, bool) {
	g, ok := r.gates[session]
	if !ok || g.finishing {
		return nil, false
	}

	return g, true
}

func (r *Registry) jobIDLocked(kind, session, lang, target string) (string, error) {
	id := encoderJobKey(kind, session, lang, target)
	if kind != "push" {
		return id, nil
	}
	if !r.admitPushTargetLocked(key(session, lang), id) {
		return "", pushLimitError(session, lang)
	}

	return id, nil
}

// Create registers the mixer and feed pacing for one session and language,
// and schedules a replay of the pair's recorded push routes: it may start
// encoder and device processes off the caller's path.
func (r *Registry) Create(
	session string, lang core.Lang, m pipeline.Template, options ...RegistrationOption,
) {
	feed := defaultFeedConfig()
	for _, option := range options {
		option(&feed)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	g, ok := r.gates[session]
	if !ok {
		ctx, cancel := context.WithCancel(r.base)
		g = &gate{ctx: ctx, cancel: cancel}
		r.gates[session] = g
	}

	id := key(session, string(lang))
	r.pairs[id] = registration{template: m, pacing: feed}

	// A re-registered pair means a (re)started lane: replay this pair's
	// recorded push routes onto the fresh mixers, off the caller's path.
	// The session gate bounds the replay and owns its drain; the Add lands
	// under r.mu while the gate is published, so Reset's Wait cannot race it.
	gateCtx := g.ctx
	g.replays.Go(func() {
		r.relaunchRoutes(gateCtx, id)
	})
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
	// Serialize with in-flight starts: startMu before mu, per the locking
	// rules on Registry.
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	g, ok := r.gates[session]
	if !ok {
		return nil, nil, nil, false
	}
	g.finishing = true

	for id, pair := range r.pairs {
		if pairOwnedBy(id, session) {
			mixers = append(mixers, pair.template)
		}
	}
	for id, job := range r.jobs {
		if job.consumesMix && jobOwnedBy(id, session) {
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
	defer r.mu.Unlock()

	prefix := sessionPushPrefix(session)
	for id := range r.routes {
		if strings.HasPrefix(id, prefix) {
			delete(r.routes, id)
		}
	}
	// Publish the teardown in the SAME lock window as the sweep — this is the
	// only delete site for routes, so anything recorded after it is
	// unreachable. A push that read a lower count crossed this teardown and
	// rememberPush refuses it.
	r.teardowns.Add(1)
}

// Reset removes every mixer of one session and stops its encoder jobs while
// keeping the session's push routes: a restarted lane re-creates the pairs
// and the routes relaunch onto them. Taking startMu first lets any in-flight
// launch finish registering (or self-kill against the finishing gate) — a
// start between its two lock phases owns a live process the job table cannot
// see yet, and a teardown that ran past it would leak that process.
func (r *Registry) Reset(session string) {
	// Retire the session gate first: once it has left the map no Create can
	// add another replay to it, and canceling it makes in-flight replays
	// abandon their remainder. The drain still happens BEFORE taking startMu —
	// a replay launching a push blocks on startMu, so waiting under the lock
	// would deadlock.
	r.mu.Lock()
	g, ok := r.gates[session]
	if ok {
		g.cancel()
		delete(r.gates, session)
	}
	r.mu.Unlock()
	if ok {
		g.replays.Wait()
	}

	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()

	for k := range r.pairs {
		if pairOwnedBy(k, session) {
			delete(r.pairs, k)
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

	// A Create that slipped in between the retire window and this one left a
	// fresh gate whose replays run against a session being torn down: cancel
	// and drop it exactly as the retire window did (its replays exit on the
	// canceled ctx, as before the per-gate drain).
	if late, present := r.gates[session]; present {
		late.cancel()
		delete(r.gates, session)
	}
	r.dropStreamsLocked(session)
	r.mu.Unlock()

	// The drain runs while startMu is still held: safe only because a job
	// goroutine takes mu alone and never startMu (locking rules on Registry).
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

// pushRoute is a user-requested output route. Routes are session intents:
// they outlive the lane's playout tree, which a failed lane drops and a
// restarted lane rebuilds, and they relaunch when their pair re-registers.
type pushRoute struct {
	session string
	lang    string
	target  string
	subs    string
}

// rememberPush records the intent behind an accepted push; teardowns is the
// count the caller read before dispatching.
//
// A push that crossed a Drop records nothing. Drop's sweep is the only site
// that deletes a route, and it can run while a Push RPC is parked anywhere
// inside its dispatch — so an intent written afterwards is unreachable
// forever: it grows the map, it pre-consumes the pair's target budget, and
// Create silently relaunches it the next time the slug exists. A missing gate
// alone cannot say that, because a push that legitimately precedes the lane's
// first pair also finds none; the teardown count is what tells the two apart.
// The gate's presence, not its liveness: a session whose playout is finishing
// is still a session, and Drop is what ends it.
//
// A STARTED push already passed the pair's bound in jobIDLocked and its route
// carries that job's key, so recording it cannot widen the pair and must never
// be refused: the caller was told the push succeeded and the registry exposes
// no un-push. A not-ready push started no process and never reached admission,
// so the bound is decided here — in the same critical section that records it,
// or two concurrent intents would both pass a check neither had committed to.
func (r *Registry) rememberPush(route pushRoute, started bool, teardowns uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, gated := r.gates[route.session]; !gated && teardowns != r.teardowns.Load() {
		r.log.Warn("push intent dropped: the session was torn down during this push",
			"session", route.session, "lang", route.lang)

		return nil
	}

	id := encoderJobKey("push", route.session, route.lang, route.target)
	if !started && !r.admitPushTargetLocked(key(route.session, route.lang), id) {
		return pushLimitError(route.session, route.lang)
	}
	r.routes[id] = route
	r.log.Debug("push route remembered",
		"session", route.session, "lang", route.lang, "routes", len(r.routes))

	return nil
}

// relaunchRoutes replays the recorded push intents of one pair onto its
// freshly registered mixers. Failures keep the intent for the next rebuild;
// a canceled session gate abandons the remainder.
func (r *Registry) relaunchRoutes(ctx context.Context, pairID string) {
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
		if ctx.Err() != nil {
			return
		}
		if err := r.push(route.session, route.lang, route.target, route.subs, false); err != nil {
			r.log.Warn("push route relaunch failed; kept for the next rebuild",
				"session", route.session, "lang", route.lang, "err", err)
		} else {
			r.log.Info("push route relaunched", "session", route.session, "lang", route.lang)
		}
	}
}

// beginStart validates the session gate, derives the job ID and detaches the
// job's predecessor under the registry lock (mu). The caller reaps the
// predecessor with reapJob OUTSIDE mu: teardown can take seconds.
func (r *Registry) beginStart(
	kind, session, lang, target string, consumesMix bool,
) (jobID string, old job, had bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if consumesMix {
		if _, ok := r.pairs[key(session, lang)]; !ok {
			return "", job{}, false, fmt.Errorf(
				"%w: no dubbed audio for %s/%s", core.ErrNotReady, session, lang)
		}
	}
	jobID, err = r.jobIDLocked(kind, session, lang, target)
	if err != nil {
		return "", job{}, false, err
	}
	if _, live := r.liveGateLocked(session); !live {
		return "", job{}, false, fmt.Errorf(
			"%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	old, had = r.jobs[jobID]
	delete(r.jobs, jobID)

	return jobID, old, had, nil
}

// reapJob cancels a detached predecessor and waits for its teardown. Both
// callers hold startMu here; that is deadlock-free only because the dying job
// goroutine takes mu alone (locking rules on Registry).
func reapJob(old job, had bool) {
	if !had {
		return
	}
	old.cancel()
	if old.done != nil {
		<-old.done
	}
}

// launchSelfFedJob runs the lifecycle of a job that feeds ITSELF: the process
// reads its own source, rides no mixer cursor and takes no PCM from the pair,
// so WaitPlayout must not wait on it — a native device process never ends on
// its own.
func (r *Registry) launchSelfFedJob(
	kind, session, lang, target string,
	start func(context.Context, *ffmpeg.Supervisor) (<-chan error, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	sup := r.sup.Load()
	if sup == nil {
		return fmt.Errorf("%w: ffmpeg is unavailable for %s/%s", core.ErrNotReady, session, lang)
	}

	jobID, old, had, err := r.beginStart(kind, session, lang, target, selfFeeding)
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
	if _, live := r.liveGateLocked(session); !live {
		r.mu.Unlock()
		cancel()

		return fmt.Errorf("%w: playout is finishing for %s/%s", core.ErrNotReady, session, lang)
	}
	gen, jobDone := r.registerJobLocked(jobID, cancel, selfFeeding)
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

	return r.launchMixFedJob(kind, session, lang, target, func(ctx context.Context) (io.WriteCloser, error) {
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

	return r.launchMixFedJob(kind, session, lang, target, func(ctx context.Context) (io.WriteCloser, error) {
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

// launchMixFedJob runs the lifecycle of a job FED FROM the pair's mix: it
// owns a mixer cursor, pumps PCM into its sink and ends at EOF, so
// WaitPlayout waits on it. The registry owns the job goroutine; cancel
// reaches it via the job context.
func (r *Registry) launchMixFedJob(
	kind, session, lang, target string, start func(context.Context) (io.WriteCloser, error),
) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	pairID := key(session, lang)
	jobID, old, had, err := r.beginStart(kind, session, lang, target, consumesPairMix)
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
	pair, ok := r.pairs[pairID]
	_, live := r.liveGateLocked(session)
	if !ok || !live {
		r.mu.Unlock()
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}
	cursor := pair.template.Cursor()
	if !cursor.BeginPlayout() {
		r.mu.Unlock()
		closeErr := out.Close()
		cancel()

		return errors.Join(core.ErrNotReady, closeErr)
	}
	gen, jobDone := r.registerJobLocked(jobID, cancel, consumesPairMix)
	r.mu.Unlock()

	go func() {
		r.runEncoderJob(jobCtx, jobID, pairID, target, out,
			encoderBinding{template: pair.template, cursor: cursor, pacing: pair.pacing}, device, start)
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

// pairSnapshot reads one pair's template without ever blocking: a job
// goroutine must stay responsive to its own cancellation while a reaper
// waits on its done channel, so it reads the registry opportunistically
// instead of queueing behind writers.
func (r *Registry) pairSnapshot(pairID, session string) (pipeline.Template, feedConfig, pairState) {
	if !r.mu.TryRLock() {
		return nil, feedConfig{}, pairBusy
	}
	next, exists := r.pairs[pairID]
	// Admission is evaluated INSIDE the window: the gate's finishing flag is
	// written under mu, so reading it after RUnlock would be a data race.
	_, live := r.liveGateLocked(session)
	r.mu.RUnlock()

	if !exists || !live {
		return nil, feedConfig{}, pairGone
	}

	return next.template, next.pacing, pairCurrent
}

// pacingForLocked answers for a pair that may not be registered yet, which is
// why the default survives: pushDevice sizes the platform device buffer
// before any pair existence check. Readers that already hold the pair's
// registration take its pacing directly.
func (r *Registry) pacingForLocked(pairID string) feedConfig {
	if pair, ok := r.pairs[pairID]; ok {
		return pair.pacing
	}

	return defaultFeedConfig()
}

func (r *Registry) pacingFor(session, lang string) feedConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.pacingForLocked(key(session, lang))
}

func (r *Registry) registerJobLocked(
	jobID string, cancel context.CancelFunc, consumesMix bool,
) (gen uint64, done chan struct{}) {
	r.nextJob++
	done = make(chan struct{})
	r.jobs[jobID] = job{cancel: cancel, done: done, gen: r.nextJob, consumesMix: consumesMix}

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
	pair, ok := r.pairs[pairID]
	g, live := r.liveGateLocked(session)
	if !ok || !live {
		r.mu.Unlock()

		return false
	}
	cursor := pair.template.Cursor()
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

	if err := r.stream(streamCtx, w, cursor, sup, pair.pacing); err != nil && streamCtx.Err() == nil {
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

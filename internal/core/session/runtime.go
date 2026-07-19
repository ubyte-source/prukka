package session

import (
	"context"
	"hash/fnv"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

// LaneStarter launches one session's pipeline and blocks until it stops.
// It calls running after the first media frame is observed; an error degrades
// the session, never the daemon.
type LaneStarter func(ctx context.Context, s *Session, running func()) error

// CleanupFunc removes one session's leftover outputs. A lane that ends on
// its own keeps its outputs downloadable, so deletion needs a path that
// works even when no lane is left to tear them down.
type CleanupFunc func(slug string)

// reconcileEvery paces the reconciliation pass, which only heals the rare
// dropped store event.
const reconcileEvery = 10 * time.Second

const (
	failedRetryBase = 10 * time.Second
	failedRetryMax  = 5 * time.Minute
)

// laneState tracks one lane's lifecycle inside the registry.
type laneState int

const (
	// laneRunning is a lane whose goroutine is (or is about to be) live.
	laneRunning laneState = iota
	// laneStopping is a canceled lane still draining its teardown.
	laneStopping
	// laneExited is a lane whose goroutine has fully returned. The entry
	// stays so reconcile does not relaunch a lane that ended on its own.
	laneExited
)

// laneReg tracks one lane; the generation disambiguates a stopped lane's
// cleanup from its replacement.
type laneReg struct {
	cancel  context.CancelFunc
	done    chan struct{}
	spec    *Session
	retryAt time.Time
	gen     uint64
	retries uint8
	state   laneState
}

// Runtime starts and stops media lanes as sessions come and go.
type Runtime struct {
	store   *Store
	start   LaneStarter
	cleanup CleanupFunc
	scrub   CleanupFunc
	log     *slog.Logger
	lanes   map[string]laneReg
	reload  chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	nextGen uint64
}

// NewRuntime wires a runtime; call Run to activate it. A nil cleanup means
// deleted sessions leave their outputs to the lane teardown alone. scrub
// clears a lane's outputs before a RESTART of the same session: unlike the
// final cleanup it must preserve durable session intents (push routes); nil
// falls back to cleanup.
func NewRuntime(store *Store, start LaneStarter, cleanup, scrub CleanupFunc, log *slog.Logger) *Runtime {
	return &Runtime{
		store:   store,
		start:   start,
		cleanup: cleanup,
		scrub:   scrub,
		log:     log,
		lanes:   map[string]laneReg{},
		reload:  make(chan struct{}, 1),
	}
}

// Reconfigure restarts active and failed lanes from the live configuration.
// Signals coalesce and never block the configuration writer.
func (r *Runtime) Reconfigure() {
	select {
	case r.reload <- struct{}{}:
	default:
	}
}

// Run reacts to store events until ctx ends and returns only after every
// lane goroutine stopped.
func (r *Runtime) Run(ctx context.Context) error {
	// Subscribing before listing means no create event can fall between the
	// snapshot and the stream; launch dedupes the overlap.
	events := r.store.Subscribe(ctx)

	snapshot := r.store.List()
	for i := range snapshot {
		r.launch(ctx, &snapshot[i])
	}

	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()

	for {
		select {
		case e, ok := <-events:
			if !ok {
				r.stopAll()
				r.wg.Wait()
				r.cleanStored()

				return nil
			}

			r.apply(ctx, &e)
		case <-tick.C:
			r.reconcile(ctx)
		case <-r.reload:
			r.reconfigure(ctx)
		}
	}
}

// cleanStored removes every current session's outputs after all lanes have
// stopped. Nothing served by this daemon should survive its lifetime.
func (r *Runtime) cleanStored() {
	stored := r.store.List()
	for i := range stored {
		r.clean(stored[i].Slug)
	}
}

// apply reacts to one store event.
func (r *Runtime) apply(ctx context.Context, e *Event) {
	switch e.Type {
	case EventCreated, EventUpdated:
		r.syncCurrent(ctx, e.Session.Slug)
	case EventDeleted:
		if _, err := r.store.Get(e.Session.Slug); err == nil {
			// A stale delete must not stop a recreated incarnation.
			r.syncCurrent(ctx, e.Session.Slug)

			return
		}
		r.stopAndWait(e.Session.Slug)
		r.clean(e.Session.Slug)
	case EventStatus:
		// Observed state never changes the lane definition.
	}
}

// syncCurrent applies the store's newest definition, not a possibly stale
// buffered event.
func (r *Runtime) syncCurrent(ctx context.Context, slug string) {
	s, err := r.store.Get(slug)
	if err != nil {
		r.stopAndWait(slug)
		r.clean(slug)

		return
	}

	r.mu.Lock()
	reg, exists := r.lanes[slug]
	r.mu.Unlock()
	if exists && (reg.spec.revision != s.revision || !sameDefinition(reg.spec, &s)) {
		r.stopAndWait(slug)
		r.scrubOutputs(slug)
	}
	r.launch(ctx, &s)
}

// reconfigure serially replaces lanes whose session is active or failed.
// A completed finite source stays completed.
func (r *Runtime) reconfigure(ctx context.Context) {
	for _, slug := range r.reconfigurable() {
		s, err := r.store.Get(slug)
		if err != nil {
			r.stopAndWait(slug)
			r.clean(slug)

			continue
		}
		if !restartable(s.Runtime().State) {
			continue
		}

		r.stopAndWait(slug)
		r.scrubOutputs(slug)

		// Configuration writes race safely with session edits: always restart
		// the current incarnation, or nothing if it was deleted meanwhile.
		s, err = r.store.Get(slug)
		if err != nil || !restartable(s.Runtime().State) {
			continue
		}
		r.launch(ctx, &s)
	}
}

func (r *Runtime) reconfigurable() []string {
	sessions := r.store.List()
	slugs := make([]string, 0, len(sessions))

	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range sessions {
		reg, exists := r.lanes[sessions[i].Slug]
		if exists && reg.state != laneStopping && restartable(sessions[i].Runtime().State) {
			slugs = append(slugs, sessions[i].Slug)
		}
	}

	return slugs
}

func restartable(state RuntimeState) bool {
	return state == StateStarting || state == StateRunning || state == StateFailed
}

// clean drops one deleted session's outputs. Idempotent against the lane
// teardown: a still-draining lane drops the same registries on its way out.
func (r *Runtime) clean(slug string) {
	if r.cleanup != nil {
		r.cleanup(slug)
	}
}

// scrubOutputs clears outputs before restarting the same session, keeping
// durable session intents alive across the restart.
func (r *Runtime) scrubOutputs(slug string) {
	if r.scrub != nil {
		r.scrub(slug)

		return
	}
	r.clean(slug)
}

// launch starts one lane; a replacement waits for its predecessor's
// teardown so old cleanup never destroys new resources.
func (r *Runtime) launch(ctx context.Context, s *Session) {
	r.mu.Lock()

	prevReg, exists := r.lanes[s.Slug]
	same, retrying := laneDisposition(prevReg, exists, s, time.Now())
	if same && !retrying {
		r.mu.Unlock()

		return
	}

	prev := prevReg.done

	laneCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.nextGen++
	gen := r.nextGen
	retries := nextRetryCount(prevReg.retries, retrying)
	// LaneStarter receives a mutable pointer; keep its snapshot separate
	// from the immutable definition reconciliation compares.
	owned := clone(s)
	run := clone(&owned)
	spec, runSpec := &owned, &run
	slug := spec.Slug
	r.lanes[slug] = laneReg{
		cancel: cancel, done: done, spec: spec, gen: gen, retries: retries, state: laneRunning,
	}
	r.mu.Unlock()
	if !r.store.bindRuntime(slug, spec.revision, gen) {
		cancel()
		close(done)
		r.forget(slug, gen)

		return
	}

	r.wg.Add(1)

	go r.runLane(laneCtx, cancel, prev, done, slug, gen, spec.revision, runSpec)
}

func laneDisposition(reg laneReg, exists bool, s *Session, now time.Time) (same, retrying bool) {
	if !exists {
		return false, false
	}
	same = reg.spec.revision == s.revision && sameDefinition(reg.spec, s)
	retrying = same && reg.state == laneExited && s.Runtime().State == StateFailed && !now.Before(reg.retryAt)

	return same, retrying
}

func nextRetryCount(previous uint8, retrying bool) uint8 {
	if retrying && previous < ^uint8(0) {
		return previous + 1
	}

	return 0
}

func (r *Runtime) runLane(
	ctx context.Context, cancel context.CancelFunc, prev <-chan struct{}, done chan<- struct{}, slug string,
	generation, revision uint64, spec *Session,
) {
	defer r.wg.Done()
	defer close(done)
	defer r.forget(slug, generation)
	defer cancel()

	if prev != nil {
		<-prev
	}
	if ctx.Err() != nil {
		return
	}

	var running sync.Once
	err := r.start(ctx, spec, func() {
		running.Do(func() {
			r.store.setRuntime(slug, revision, generation, StateRunning, nil)
		})
	})
	r.finishLane(ctx, slug, revision, generation, spec.Source.URL, err)
}

func (r *Runtime) finishLane(
	ctx context.Context, slug string, revision, generation uint64, source string, err error,
) {
	switch {
	case err == nil:
		r.store.setRuntime(slug, revision, generation, StateFinished, nil)
		r.log.Info("lane finished", "session", slug)
	case ctx.Err() != nil:
		// Session removed or daemon stopping: silence is correct.
	default:
		if r.store.setRuntime(slug, revision, generation, StateFailed, err, source) {
			r.scheduleRetry(slug, generation)
		}
		r.log.Warn("lane unavailable", "session", slug, "reason", sanitizeRuntimeError(err, source))
	}
}

func (r *Runtime) scheduleRetry(slug string, generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.lanes[slug]
	if !ok || reg.gen != generation {
		return
	}
	reg.retryAt = time.Now().Add(failedRetryDelay(slug, reg.retries))
	r.lanes[slug] = reg
}

func failedRetryDelay(slug string, retries uint8) time.Duration {
	delay := failedRetryBase
	for range retries {
		if delay >= failedRetryMax/2 {
			delay = failedRetryMax

			break
		}
		delay *= 2
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(slug))
	jitter := time.Duration(hash.Sum32()%1_000) * delay / 5_000

	return min(delay+jitter, failedRetryMax)
}

// stop cancels one session's lane if it is running; an exited leftover is
// cleared so a later create starts fresh.
func (r *Runtime) stop(slug string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.lanes[slug]
	if !ok {
		return nil
	}

	switch reg.state {
	case laneRunning:
		reg.cancel()
		reg.state = laneStopping
		r.lanes[slug] = reg
	case laneStopping:
		// Already draining; its goroutine owns the rest.
	case laneExited:
		delete(r.lanes, slug)
	}

	return reg.done
}

func (r *Runtime) stopAndWait(slug string) {
	done := r.stop(slug)
	if done == nil {
		return
	}
	<-done

	r.mu.Lock()
	if reg, ok := r.lanes[slug]; ok && reg.done == done && reg.state == laneExited {
		delete(r.lanes, slug)
	}
	r.mu.Unlock()
}

// stopAll cancels every running lane; Run waits for the goroutines.
func (r *Runtime) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for slug, reg := range r.lanes {
		if reg.state == laneRunning {
			reg.cancel()
			reg.state = laneStopping
			r.lanes[slug] = reg
		}
	}
}

// forget marks a lane exited (its own generation only); the entry stays so
// reconcile does not relaunch a self-ended lane.
func (r *Runtime) forget(slug string, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if reg, ok := r.lanes[slug]; ok && reg.gen == gen {
		reg.state = laneExited
		r.lanes[slug] = reg
	}
}

// reconcile heals store/lane divergence after dropped events; a lane that
// exited on its own stays down.
func (r *Runtime) reconcile(ctx context.Context) {
	sessions := r.store.List()

	current := make(map[string]*Session, len(sessions))
	for i := range sessions {
		current[sessions[i].Slug] = &sessions[i]
	}

	for slug, s := range current {
		r.mu.Lock()
		reg, ok := r.lanes[slug]
		r.mu.Unlock()

		switch {
		case !ok:
			r.launch(ctx, s) // missed EventCreated
		case reg.spec.revision != s.revision || !sameDefinition(reg.spec, s):
			r.stopAndWait(slug) // missed EventUpdated
			r.scrubOutputs(slug)
			r.launch(ctx, s)
		case reg.state == laneExited && s.Runtime().State == StateFailed &&
			!time.Now().Before(reg.retryAt):
			r.scrubOutputs(slug)
			r.launch(ctx, s)
		}
	}

	for _, slug := range r.orphans(current) {
		r.stopAndWait(slug) // missed EventDeleted
		r.clean(slug)
	}
}

func sameDefinition(a, b *Session) bool {
	return a.Slug == b.Slug && a.Profile == b.Profile && a.Source == b.Source &&
		slices.Equal(a.Langs, b.Langs) && maps.Equal(a.Flags, b.Flags) && a.Delay == b.Delay
}

// orphans lists lanes without a stored session, clearing exited leftovers
// along the way; every returned slug still needs its outputs cleaned.
func (r *Runtime) orphans(current map[string]*Session) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string

	for slug, reg := range r.lanes {
		if _, ok := current[slug]; ok {
			continue
		}

		switch reg.state {
		case laneRunning:
			out = append(out, slug)
		case laneExited:
			// Self-ended before the missed delete: no teardown is left to
			// drop its outputs, so the cleanup below is the only one.
			delete(r.lanes, slug)
			out = append(out, slug)
		case laneStopping:
			// Draining; the next pass sees it exited and clears it.
		}
	}

	return out
}

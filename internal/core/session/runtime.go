package session

import (
	"context"
	"hash/fnv"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/besteffort"
)

// LaneStarter launches one session's pipeline, blocks until it stops, and
// calls running once the first media frame is observed.
type LaneStarter func(ctx context.Context, s *Session, running func()) error

// LaneOutputs removes one session's leftover outputs: Drop forgets them
// entirely, Scrub clears them for a restart and preserves the durable session
// intents the rebuilt lane relaunches on.
type LaneOutputs interface {
	Drop(slug string)
	Scrub(slug string)
}

const reconcileEvery = 10 * time.Second

const (
	failedRetryBase = 10 * time.Second
	failedRetryMax  = 5 * time.Minute
)

// laneState tracks one lane's lifecycle inside the registry.
type laneState int

const (
	laneRunning laneState = iota
	laneStopping
	laneExited
)

// revision numbers one session definition; the store stamps a new one on
// every accepted write.
type revision uint64

// generation numbers one lane incarnation of a session.
type generation uint64

// laneID identifies one lane incarnation.
type laneID struct {
	slug     string
	revision revision
	gen      generation
}

// laneReg tracks one lane in the registry.
type laneReg struct {
	cancel  context.CancelFunc
	done    chan struct{}
	spec    *Session
	retryAt time.Time
	gen     generation
	retries uint8
	state   laneState
}

// laneDeps carries one lane incarnation from the launch slot into its own
// goroutine.
type laneDeps struct {
	spec     *Session
	cancel   context.CancelFunc
	prev     <-chan struct{}
	done     chan<- struct{}
	preStart func()
	id       laneID
}

// Runtime starts and stops media lanes as sessions come and go.
type Runtime struct {
	store   *Store
	start   LaneStarter
	outputs LaneOutputs
	log     *slog.Logger
	lanes   map[string]laneReg
	reload  chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	nextGen generation
}

// RuntimeDeps carries the runtime's collaborators; Outputs may be nil.
type RuntimeDeps struct {
	Store   *Store
	Start   LaneStarter
	Outputs LaneOutputs
	Log     *slog.Logger
}

// NewRuntime wires a runtime; call Run to activate it.
func NewRuntime(deps *RuntimeDeps) *Runtime {
	return &Runtime{
		store:   deps.Store,
		start:   deps.Start,
		outputs: deps.Outputs,
		log:     deps.Log,
		lanes:   map[string]laneReg{},
		reload:  make(chan struct{}, 1),
	}
}

// Reconfigure restarts active and failed lanes from the live configuration;
// signals coalesce and never block the caller.
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
		r.launch(ctx, &snapshot[i], nil)
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

// cleanStored removes every session's outputs after all lanes have stopped.
func (r *Runtime) cleanStored() {
	stored := r.store.List()
	for i := range stored {
		r.clean(stored[i].Slug)
	}
}

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
		r.stop(slug)
		r.launch(ctx, &s, func() { r.scrubOutputs(slug) })

		return
	}
	r.launch(ctx, &s, nil)
}

// reconfigure serially replaces lanes whose session is active or failed.
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

		r.stop(slug)
		r.launch(ctx, &s, func() { r.scrubOutputs(slug) })
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

// clean drops one deleted session's outputs.
func (r *Runtime) clean(slug string) {
	if r.outputs != nil {
		r.outputs.Drop(slug)
	}
}

// scrubOutputs clears outputs before restarting the same session.
func (r *Runtime) scrubOutputs(slug string) {
	if r.outputs != nil {
		r.outputs.Scrub(slug)
	}
}

// launch starts one lane; a replacement waits out its predecessor's teardown,
// then runs preStart before starting.
func (r *Runtime) launch(ctx context.Context, s *Session, preStart func()) {
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
	retries := nextRetryCount(prevReg.retries, retrying)
	// LaneStarter receives a mutable pointer; keep its snapshot separate
	// from the immutable definition reconciliation compares.
	owned := clone(s)
	run := clone(&owned)
	spec, runSpec := &owned, &run
	id := laneID{slug: spec.Slug, revision: spec.revision, gen: r.nextGen}
	r.lanes[id.slug] = laneReg{
		cancel:  cancel,
		done:    done,
		spec:    spec,
		gen:     id.gen,
		retries: retries,
		state:   laneRunning,
	}
	r.mu.Unlock()

	lane := &laneDeps{spec: runSpec, cancel: cancel, prev: prev, done: done, preStart: preStart, id: id}
	if !r.store.bindRuntime(id) {
		cancel()
		r.wg.Add(1)

		go r.abandonLane(lane)

		return
	}

	r.wg.Add(1)

	go r.runLane(laneCtx, lane)
}

// abandonLane retires a lane that never ran, in runLane's teardown order, so
// the drain chain stays intact for whoever waits on done.
func (r *Runtime) abandonLane(d *laneDeps) {
	defer r.wg.Done()
	defer close(d.done)
	defer r.forget(d.id)

	if d.prev != nil {
		<-d.prev
	}
}

func laneDisposition(reg laneReg, exists bool, s *Session, now time.Time) (same, retrying bool) {
	if !exists {
		return false, false
	}
	// A draining lane is a corpse, never "this lane already runs".
	same = reg.state != laneStopping &&
		reg.spec.revision == s.revision && sameDefinition(reg.spec, s)
	retrying = same && reg.state == laneExited && s.Runtime().State == StateFailed && !now.Before(reg.retryAt)

	return same, retrying
}

func nextRetryCount(previous uint8, retrying bool) uint8 {
	if retrying && previous < ^uint8(0) {
		return previous + 1
	}

	return 0
}

func (r *Runtime) runLane(ctx context.Context, d *laneDeps) {
	defer r.wg.Done()
	defer close(d.done)
	defer r.forget(d.id)
	defer d.cancel()

	if d.prev != nil {
		<-d.prev
	}
	if ctx.Err() != nil {
		return
	}
	if d.preStart != nil {
		d.preStart()
	}

	var running sync.Once
	err := r.start(ctx, d.spec, func() {
		running.Do(func() {
			r.store.setRuntime(d.id, StateRunning, nil)
		})
	})
	r.finishLane(ctx, d, err)
}

func (r *Runtime) finishLane(ctx context.Context, d *laneDeps, err error) {
	switch {
	case err == nil:
		r.store.setRuntime(d.id, StateFinished, nil)
		r.log.Info("lane finished", "session", d.id.slug)
	case ctx.Err() != nil:
		// Session removed or daemon stopping: silence is correct.
	default:
		if r.store.setRuntime(d.id, StateFailed, err) {
			r.scheduleRetry(d.id)
		}
		r.log.Warn("lane unavailable", "session", d.id.slug, "reason", sanitizeRuntimeError(err))
	}
}

func (r *Runtime) scheduleRetry(id laneID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.lanes[id.slug]
	if !ok || reg.gen != id.gen {
		return
	}
	reg.retryAt = time.Now().Add(failedRetryDelay(id.slug, reg.retries))
	r.lanes[id.slug] = reg
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
	_, writeErr := hash.Write([]byte(slug))
	besteffort.Ignore(writeErr)

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

// stopAndWait blocks on the terminal paths (deletion, vanished session):
// clean() must never race a recreated session's registrations. Restarts do not
// wait; their scrub rides the replacement lane.
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
func (r *Runtime) forget(id laneID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if reg, ok := r.lanes[id.slug]; ok && reg.gen == id.gen {
		reg.state = laneExited
		r.lanes[id.slug] = reg
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
			r.launch(ctx, s, nil) // missed EventCreated
		case reg.spec.revision != s.revision || !sameDefinition(reg.spec, s):
			r.stop(slug) // missed EventUpdated
			r.launch(ctx, s, func() { r.scrubOutputs(slug) })
		case reg.state == laneExited && s.Runtime().State == StateFailed &&
			!time.Now().Before(reg.retryAt):
			r.launch(ctx, s, func() { r.scrubOutputs(slug) })
		}
	}

	for _, slug := range r.orphans(current) {
		r.stopAndWait(slug) // missed EventDeleted
		r.clean(slug)
	}
}

func sameDefinition(a, b *Session) bool {
	return a.Slug == b.Slug && a.Profile == b.Profile && a.Source == b.Source &&
		slices.Equal(a.Langs, b.Langs) && a.Delay == b.Delay && sameMedia(a, b)
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
			// Self-ended: no teardown is left to drop its outputs.
			delete(r.lanes, slug)
			out = append(out, slug)
		case laneStopping:
			// Draining; the next pass sees it exited and clears it.
		}
	}

	return out
}

package session

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// LaneStarter launches one session's pipeline and blocks until it stops;
// an error degrades the session, never the daemon.
type LaneStarter func(ctx context.Context, s *Session) error

// reconcileEvery paces the reconciliation pass, which only heals the rare
// dropped store event.
const reconcileEvery = 10 * time.Second

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
	cancel context.CancelFunc
	done   chan struct{}
	langs  []core.Lang
	gen    uint64
	state  laneState
}

// Runtime starts and stops media lanes as sessions come and go.
type Runtime struct {
	store   *Store
	start   LaneStarter
	log     *slog.Logger
	lanes   map[string]laneReg
	wg      sync.WaitGroup
	mu      sync.Mutex
	nextGen uint64
}

// NewRuntime wires a runtime; call Run to activate it.
func NewRuntime(store *Store, start LaneStarter, log *slog.Logger) *Runtime {
	return &Runtime{
		store: store,
		start: start,
		log:   log,
		lanes: map[string]laneReg{},
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

				return nil
			}

			r.apply(ctx, &e)
		case <-tick.C:
			r.reconcile(ctx)
		}
	}
}

// apply reacts to one store event.
func (r *Runtime) apply(ctx context.Context, e *Event) {
	s := e.Session

	switch e.Type {
	case EventCreated:
		r.launch(ctx, &s)
	case EventUpdated:
		// The lane's language set is fixed at start: restart on change.
		r.stop(s.Slug)
		r.launch(ctx, &s)
	case EventDeleted:
		r.stop(s.Slug)
	}
}

// launch starts one lane; a replacement waits for its predecessor's
// teardown so old cleanup never destroys new resources.
func (r *Runtime) launch(ctx context.Context, s *Session) {
	r.mu.Lock()

	prevReg, exists := r.lanes[s.Slug]
	if exists && prevReg.state == laneRunning {
		r.mu.Unlock()

		return
	}

	var prev chan struct{}
	if exists {
		prev = prevReg.done
	}

	laneCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.nextGen++
	gen := r.nextGen
	r.lanes[s.Slug] = laneReg{
		cancel: cancel, done: done, langs: slices.Clone(s.Langs), gen: gen, state: laneRunning,
	}
	r.mu.Unlock()

	r.wg.Add(1)

	go func() {
		defer r.wg.Done()
		defer close(done)
		defer r.forget(s.Slug, gen)

		if prev != nil {
			<-prev
		}

		if laneCtx.Err() != nil {
			return // stopped while waiting for the predecessor
		}

		err := r.start(laneCtx, s)

		switch {
		case err == nil:
			r.log.Info("lane finished", "session", s.Slug)
		case errors.Is(err, context.Canceled):
			// Session removed or daemon stopping: silence is correct.
		default:
			r.log.Warn("lane unavailable", "session", s.Slug, "reason", err)
		}
	}()
}

// stop cancels one session's lane if it is running; an exited leftover is
// cleared so a later create starts fresh.
func (r *Runtime) stop(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.lanes[slug]
	if !ok {
		return
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
		case reg.state == laneRunning && !slices.Equal(reg.langs, s.Langs):
			r.stop(slug) // missed EventUpdated
			r.launch(ctx, s)
		}
	}

	for _, slug := range r.orphans(current) {
		r.stop(slug) // missed EventDeleted
	}
}

// orphans lists running lanes without a stored session, clearing exited
// leftovers along the way.
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
			delete(r.lanes, slug)
		case laneStopping:
			// Draining; the next pass sees it exited and clears it.
		}
	}

	return out
}

package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// countingStarter records starts per slug and blocks lanes until canceled.
type countingStarter struct {
	starts map[string]int
	mu     sync.Mutex
}

type reconfigureRecorder struct {
	starts  map[string]int
	cleaned map[string]int
	mu      sync.Mutex
}

func newReconfigureRecorder() *reconfigureRecorder {
	return &reconfigureRecorder{starts: map[string]int{}, cleaned: map[string]int{}}
}

func (r *reconfigureRecorder) starter(ctx context.Context, s *Session, running func()) error {
	r.mu.Lock()
	r.starts[s.Slug]++
	attempt := r.starts[s.Slug]
	r.mu.Unlock()

	if s.Slug == "failed" && attempt == 1 {
		return errors.New("temporary failure")
	}
	if s.Slug == "finished" {
		return nil
	}

	running()
	<-ctx.Done()

	return ctx.Err()
}

func (r *reconfigureRecorder) cleanup(slug string) {
	r.mu.Lock()
	r.cleaned[slug]++
	r.mu.Unlock()
}

func (r *reconfigureRecorder) initialStarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.starts["running"] == 1 && r.starts["failed"] == 1 && r.starts["finished"] == 1
}

func (r *reconfigureRecorder) restarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.starts["running"] >= 2 && r.starts["failed"] >= 2
}

func (r *reconfigureRecorder) assertSelective(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.starts["finished"] != 1 {
		t.Fatalf("finished lane starts = %d, want 1", r.starts["finished"])
	}
	if r.cleaned["running"] != 1 || r.cleaned["failed"] != 1 || r.cleaned["finished"] != 0 {
		t.Fatalf("cleanup calls = %v before shutdown, want running/failed only", r.cleaned)
	}
}

type concurrentReconfigureStarter struct {
	canceled      chan struct{}
	release       chan struct{}
	latest        atomic.Value
	starts        atomic.Int32
	updatedStarts atomic.Int32
}

func newConcurrentReconfigureStarter() *concurrentReconfigureStarter {
	return &concurrentReconfigureStarter{canceled: make(chan struct{}), release: make(chan struct{})}
}

func (s *concurrentReconfigureStarter) starter(
	ctx context.Context, session *Session, running func(),
) error {
	s.starts.Add(1)
	attempt := int32(0)
	if session.Slug == "a-update" {
		attempt = s.updatedStarts.Add(1)
		s.latest.Store(string(session.Langs[0]))
	}
	running()
	<-ctx.Done()
	if session.Slug == "a-update" && attempt == 1 {
		close(s.canceled)
		<-s.release
	}

	return ctx.Err()
}

func (s *concurrentReconfigureStarter) latestRunning(store *Store) bool {
	current, err := store.Get("a-update")

	return err == nil && s.starts.Load() == 3 && current.Runtime().State == StateRunning &&
		s.latest.Load() == "fr"
}

func createTestSessions(t *testing.T, store *Store, slugs ...string) {
	t.Helper()
	for _, slug := range slugs {
		if err := store.Create(testSession(slug)); err != nil {
			t.Fatalf("Create(%q) returned error: %v", slug, err)
		}
	}
}

func terminalStatesReady(store *Store) bool {
	failed, failedErr := store.Get("failed")
	finished, finishedErr := store.Get("finished")

	return failedErr == nil && finishedErr == nil &&
		failed.Runtime().State == StateFailed && finished.Runtime().State == StateFinished
}

func (c *countingStarter) starter(ctx context.Context, s *Session, running func()) error {
	c.mu.Lock()
	c.starts[s.Slug]++
	c.mu.Unlock()
	running()

	<-ctx.Done()

	return ctx.Err()
}

func (c *countingStarter) count(slug string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.starts[slug]
}

// waitFor polls until check passes or the deadline hits.
func waitFor(t *testing.T, check func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// testSession builds a valid session for white-box runtime tests.
func testSession(slug string) *Session {
	return &Session{
		Slug:    slug,
		Profile: ProfileBroadcast,
		Source:  core.SourceSpec{URL: "rtmp://127.0.0.1/live"},
		Langs:   []core.Lang{"en"},
	}
}

// TestReconcileHealsMissedEvents: missed create launches, missed update
// restarts, missed delete stops.
func TestReconcileHealsMissedEvents(t *testing.T) {
	t.Parallel()

	store := NewStore()
	starts := &countingStarter{starts: map[string]int{}}
	rt := NewRuntime(store, starts.starter, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Missed create: the session exists, no lane. Reconcile launches it.
	if err := store.Create(testSession("ghosted")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	rt.reconcile(ctx)
	waitFor(t, func() bool { return starts.count("ghosted") == 1 }, "lane for missed create")

	// Missed update: the stored language set changed behind the lane's back.
	if _, err := store.UpdateLangs("ghosted", []core.Lang{"fr"}, []core.Lang{"en"}); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}

	// Consume the pending restart the event would have done, as if dropped.
	rt.reconcile(ctx)
	waitFor(t, func() bool { return starts.count("ghosted") == 2 }, "restart for missed update")

	// Missed delete: the session is gone, the lane still runs.
	if err := store.Delete("ghosted"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	rt.reconcile(ctx)
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()

		reg, ok := rt.lanes["ghosted"]

		return !ok || reg.state != laneRunning
	}, "stop for missed delete")

	rt.stopAll()
	rt.wg.Wait()
}

// TestReconcileLeavesSelfEndedLanesDown: a self-ended lane must not be
// relaunched while its session exists.
func TestReconcileLeavesSelfEndedLanesDown(t *testing.T) {
	t.Parallel()

	store := NewStore()

	var mu sync.Mutex

	runs := 0
	rt := NewRuntime(store, func(context.Context, *Session, func()) error {
		mu.Lock()
		runs++
		mu.Unlock()

		return nil // the source ended on its own
	}, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := store.Create(testSession("ended")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	stored, err := store.Get("ended")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored)
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()

		return rt.lanes["ended"].state == laneExited
	}, "lane exit")

	rt.reconcile(ctx)
	rt.wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if runs != 1 {
		t.Fatalf("lane ran %d times, want 1 (reconcile must not resurrect self-ended lanes)", runs)
	}
}

func TestReconcileRestartsAnUpdatedSelfEndedLane(t *testing.T) {
	t.Parallel()

	store := NewStore()
	var runs atomic.Int32
	rt := NewRuntime(store, func(context.Context, *Session, func()) error {
		runs.Add(1)

		return nil
	}, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("updated-end")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("updated-end")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored)
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()

		return rt.lanes[stored.Slug].state == laneExited
	}, "first lane exit")

	if _, err := store.UpdateLangs(stored.Slug, []core.Lang{"fr"}, nil); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	rt.reconcile(ctx)
	waitFor(t, func() bool { return runs.Load() == 2 }, "restart after missed update")
	rt.wg.Wait()
}

// TestReconcileRestartsRecreatedSession catches a dropped delete/create
// pair whose replacement has the same slug and definition.
func TestReconcileRestartsRecreatedSession(t *testing.T) {
	t.Parallel()

	store := NewStore()
	starts := &countingStarter{starts: map[string]int{}}
	rt := NewRuntime(store, starts.starter, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := store.Create(testSession("recreated")); err != nil {
		t.Fatalf("first Create returned error: %v", err)
	}
	first, err := store.Get("recreated")
	if err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	rt.launch(ctx, &first)
	waitFor(t, func() bool { return starts.count("recreated") == 1 }, "first incarnation")

	if err := store.Delete("recreated"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if err := store.Create(testSession("recreated")); err != nil {
		t.Fatalf("second Create returned error: %v", err)
	}

	// No event is applied: reconcile must distinguish the new incarnation
	// even though every public field is identical.
	rt.reconcile(ctx)
	waitFor(t, func() bool { return starts.count("recreated") == 2 }, "replacement incarnation")

	rt.stopAll()
	rt.wg.Wait()
}

func TestLaneStarterCannotMutateReconcileSnapshot(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	store := NewStore()
	rt := NewRuntime(store, func(ctx context.Context, s *Session, running func()) error {
		s.Langs[0] = "fr"
		close(started)
		running()
		<-ctx.Done()

		return ctx.Err()
	}, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("owned")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("owned")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lane did not start")
	}

	rt.mu.Lock()
	got := rt.lanes["owned"].spec.Langs[0]
	rt.mu.Unlock()
	if got != "en" {
		t.Fatalf("reconcile snapshot was mutated through LaneStarter: %q", got)
	}

	rt.stopAll()
	rt.wg.Wait()
}

func TestReconcileRetriesFailedLaneWhenDue(t *testing.T) {
	t.Parallel()

	store := NewStore()
	var starts atomic.Int32
	rt := NewRuntime(store, func(ctx context.Context, _ *Session, running func()) error {
		if starts.Add(1) == 1 {
			return errors.New("temporary failure")
		}
		running()
		<-ctx.Done()

		return ctx.Err()
	}, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("retry-due")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("retry-due")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored)
	waitFor(t, func() bool {
		current, getErr := store.Get("retry-due")
		if getErr != nil || current.Runtime().State != StateFailed {
			return false
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()

		reg := rt.lanes["retry-due"]
		reg.retryAt = time.Time{}
		rt.lanes["retry-due"] = reg

		return reg.state == laneExited
	}, "failed lane exit")

	rt.reconcile(ctx)
	waitFor(t, func() bool {
		current, getErr := store.Get("retry-due")

		return getErr == nil && starts.Load() == 2 && current.Runtime().State == StateRunning
	}, "failed lane retry")

	rt.stopAll()
	rt.wg.Wait()
}

func TestRetrySignalMakesFailedLaneImmediatelyEligible(t *testing.T) {
	t.Parallel()

	store := NewStore()
	var starts atomic.Int32
	rt := NewRuntime(store, func(context.Context, *Session, func()) error {
		starts.Add(1)

		return errors.New("temporary failure")
	}, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	if err := store.Create(testSession("retry-signal")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	waitFor(t, func() bool {
		current, err := store.Get("retry-signal")
		if err != nil || current.Runtime().State != StateFailed || starts.Load() != 1 {
			return false
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()

		return rt.lanes["retry-signal"].state == laneExited
	}, "first failed lane exit")
	rt.RetryFailed()
	waitFor(t, func() bool { return starts.Load() == 2 }, "signaled retry")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReconfigureRestartsActiveAndFailedLanesOnly(t *testing.T) {
	t.Parallel()

	store := NewStore()
	recorder := newReconfigureRecorder()
	rt := NewRuntime(store, recorder.starter, recorder.cleanup, slog.New(slog.DiscardHandler))
	createTestSessions(t, store, "running", "failed", "finished")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	waitFor(t, recorder.initialStarted, "initial lane starts")
	waitFor(t, func() bool { return terminalStatesReady(store) }, "terminal lane states")

	rt.Reconfigure()
	waitFor(t, recorder.restarted, "reconfigured lane starts")
	recorder.assertSelective(t)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReconfigureUsesLatestConcurrentSessionState(t *testing.T) {
	t.Parallel()

	store := NewStore()
	starter := newConcurrentReconfigureStarter()
	rt := NewRuntime(store, starter.starter, nil, slog.New(slog.DiscardHandler))
	createTestSessions(t, store, "a-update", "b-delete")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	waitFor(t, func() bool { return starter.starts.Load() == 2 }, "initial lane starts")
	rt.Reconfigure()

	select {
	case <-starter.canceled:
	case <-time.After(time.Second):
		t.Fatal("reconfigure did not stop the first lane")
	}
	if _, err := store.UpdateLangs("a-update", []core.Lang{"fr"}, []core.Lang{"en"}); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	if err := store.Delete("b-delete"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	close(starter.release)

	waitFor(t, func() bool { return starter.latestRunning(store) }, "latest updated incarnation")
	time.Sleep(25 * time.Millisecond)
	if got := starter.starts.Load(); got != 3 {
		t.Fatalf("stale events caused %d total starts, want 3", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReconfigureSignalsCoalesce(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(NewStore(), nil, nil, slog.New(slog.DiscardHandler))
	for range 10 {
		rt.Reconfigure()
	}
	if got := len(rt.reload); got != 1 {
		t.Fatalf("queued reconfigure signals = %d, want 1", got)
	}
}

func TestFailedRetryDelayIsDeterministicAndBounded(t *testing.T) {
	t.Parallel()

	previous := time.Duration(0)
	for retry := range uint8(10) {
		got := failedRetryDelay("retry-delay", retry)
		if got < previous || got < failedRetryBase || got > failedRetryMax {
			t.Fatalf("retry %d delay = %v after %v; want monotonic in [%v, %v]",
				retry, got, previous, failedRetryBase, failedRetryMax)
		}
		if again := failedRetryDelay("retry-delay", retry); again != got {
			t.Fatalf("retry %d delay changed from %v to %v", retry, got, again)
		}
		previous = got
	}
}

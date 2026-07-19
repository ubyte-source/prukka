package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ubyte-source/prukka/internal/core"

	"github.com/ubyte-source/prukka/internal/testkit"
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

	return err == nil && current.Runtime().State == StateRunning && s.latest.Load() == "fr"
}

// assertSettledOnLatest pins the quiescence contract: reactivity allows one
// transient stale incarnation that its own event supersedes, but the LATEST
// definition must be the last one started and nothing may move after that.
// A sleep could only say "nothing moved for 50ms on this machine"; inside a
// bubble synctest.Wait says no goroutine can move at all.
func (s *concurrentReconfigureStarter) assertSettledOnLatest(t *testing.T, store *Store) {
	t.Helper()

	synctest.Wait()
	if !s.latestRunning(store) {
		t.Fatal("the latest updated incarnation is not the running one")
	}

	settled := s.starts.Load()
	synctest.Wait()
	if got := s.starts.Load(); got != settled {
		t.Fatalf("lanes kept churning after quiescence: %d -> %d starts", settled, got)
	}
	if got := s.latest.Load(); got != "fr" {
		t.Fatalf("a stale incarnation started after the latest: langs %v", got)
	}
	if got := s.updatedStarts.Load(); got > 3 {
		t.Fatalf("a-update started %d times, want at most 3", got)
	}
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
	testkit.Eventually(t, 2*time.Second, check, what)
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

// TestFailedLaneRetriesOnTheReconcileTick drives both clocks the runtime
// owns — the reconcileEvery ticker Run wires up and the failed-lane backoff
// gate reconcile compares against — inside a synctest bubble. On the fake
// clock the ten-second tick and the ten-second-plus-jitter retryAt are
// instant and exact, so this is the wiring itself under test rather than
// reconcile called by hand.
func TestFailedLaneRetriesOnTheReconcileTick(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var starts atomic.Int32
		retried := make(chan struct{})
		store := NewStore()
		rt := NewRuntime(store, func(context.Context, *Session, func()) error {
			if starts.Add(1) == 2 {
				close(retried)
			}

			return errors.New("capture unavailable")
		}, nil, nil, slog.New(slog.DiscardHandler))

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() { done <- rt.Run(ctx) }()

		began := time.Now()
		if err := store.Create(testSession("flaky")); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// No polling and no sleep: the bubble only advances its clock once
		// every goroutine is durably blocked, so this returns the instant
		// the backoff and a tick have both elapsed in fake time. The bound is
		// fake time too, and it is load-bearing — a runtime that never
		// retries leaves the ticker firing forever, which is a live hang the
		// bubble cannot report as a deadlock, so without a deadline a broken
		// gate would run out the whole test binary's clock instead of
		// failing here.
		select {
		case <-retried:
		case <-time.After(2 * failedRetryMax):
			t.Fatalf("no retry within %v of fake time; the reconcile tick or the retryAt gate is unwired",
				2*failedRetryMax)
		}

		if waited := time.Since(began); waited < failedRetryBase {
			t.Fatalf("retry ran after %v, want at least the %v backoff", waited, failedRetryBase)
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	})
}

// TestReconcileHealsMissedEvents: missed create launches, missed update
// restarts, missed delete stops.
func TestReconcileHealsMissedEvents(t *testing.T) {
	t.Parallel()

	store := NewStore()
	starts := &countingStarter{starts: map[string]int{}}
	rt := NewRuntime(store, starts.starter, nil, nil, slog.New(slog.DiscardHandler))

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
	}, nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := store.Create(testSession("ended")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	stored, err := store.Get("ended")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored, nil)
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
	}, nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("updated-end")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("updated-end")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored, nil)
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
	rt := NewRuntime(store, starts.starter, nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := store.Create(testSession("recreated")); err != nil {
		t.Fatalf("first Create returned error: %v", err)
	}
	first, err := store.Get("recreated")
	if err != nil {
		t.Fatalf("first Get returned error: %v", err)
	}
	rt.launch(ctx, &first, nil)
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
	}, nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("owned")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("owned")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored, nil)

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
	}, nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("retry-due")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("retry-due")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored, nil)
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

func TestReconfigureRestartsActiveAndFailedLanesOnly(t *testing.T) {
	t.Parallel()

	store := NewStore()
	recorder := newReconfigureRecorder()
	rt := NewRuntime(store, recorder.starter, recorder.cleanup, nil, slog.New(slog.DiscardHandler))
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

// TestReconfigureUsesLatestConcurrentSessionState runs in a bubble so every
// wait is synctest.Wait rather than a poll: the assertions here are about
// settling — one transient stale incarnation is allowed, the latest
// definition must be last — and settling is what a bubble answers exactly.
// Nothing here blocks on a bare channel, so the fake clock never advances
// and the reconcile ticker stays out of the scenario.
func TestReconfigureUsesLatestConcurrentSessionState(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := NewStore()
		starter := newConcurrentReconfigureStarter()
		rt := NewRuntime(store, starter.starter, nil, nil, slog.New(slog.DiscardHandler))
		createTestSessions(t, store, "a-update", "b-delete")

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- rt.Run(ctx) }()

		synctest.Wait()
		if got := starter.starts.Load(); got != 2 {
			t.Fatalf("initial lane starts = %d, want 2", got)
		}

		rt.Reconfigure()
		synctest.Wait()
		select {
		case <-starter.canceled:
		default:
			t.Fatal("reconfigure did not stop the first lane")
		}

		if _, err := store.UpdateLangs("a-update", []core.Lang{"fr"}, []core.Lang{"en"}); err != nil {
			t.Fatalf("UpdateLangs returned error: %v", err)
		}
		if err := store.Delete("b-delete"); err != nil {
			t.Fatalf("Delete returned error: %v", err)
		}
		close(starter.release)

		starter.assertSettledOnLatest(t, store)

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	})
}

func TestReconfigureSignalsCoalesce(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(NewStore(), nil, nil, nil, slog.New(slog.DiscardHandler))
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

// TestRetryScrubsWhileDeleteCleans: a failed lane's retry must clear its
// outputs with the scrub hook — the one that preserves durable session
// intents such as push routes — while a deletion runs the final cleanup.
// TestLostBindRaceKeepsTheDrainChainIntact: a generation that loses the
// store's revision race must not unlink a still-draining predecessor — its
// done link closes only after the predecessor's, so a successor can never
// scrub and start over a live teardown.
func TestLostBindRaceKeepsTheDrainChainIntact(t *testing.T) {
	t.Parallel()

	store := NewStore()
	rt := NewRuntime(store, func(context.Context, *Session, func()) error {
		return nil
	}, nil, nil, slog.New(slog.DiscardHandler))

	if err := store.Create(testSession("race")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	current, err := store.Get("race")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	// Predecessor A drains: its done is the link every successor waits on.
	prevDone := make(chan struct{})
	rt.mu.Lock()
	rt.lanes["race"] = laneReg{
		cancel: func() {}, done: prevDone, spec: &current, gen: 1, state: laneStopping,
	}
	rt.nextGen = 1
	rt.mu.Unlock()

	// B carries a revision the store does not hold, so bindRuntime rejects it.
	stale := clone(&current)
	stale.revision++
	rt.launch(t.Context(), &stale, nil)

	rt.mu.Lock()
	state := rt.lanes["race"].state
	rt.mu.Unlock()
	if state == laneExited {
		t.Fatal("rejected lane retired itself while its predecessor still drains")
	}

	close(prevDone)
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()

		return rt.lanes["race"].state == laneExited
	}, "rejected lane to retire after the predecessor drained")
	rt.wg.Wait()
}

func TestRetryScrubsWhileDeleteCleans(t *testing.T) {
	t.Parallel()

	store := NewStore()
	var starts atomic.Int32
	var scrubs, cleans atomic.Int32
	rt := NewRuntime(store, func(ctx context.Context, _ *Session, running func()) error {
		if starts.Add(1) == 1 {
			return errors.New("temporary failure")
		}
		running()
		<-ctx.Done()

		return ctx.Err()
	},
		func(string) { cleans.Add(1) },
		func(string) { scrubs.Add(1) },
		slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.Create(testSession("scrub-vs-clean")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	stored, err := store.Get("scrub-vs-clean")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	rt.launch(ctx, &stored, nil)
	waitFor(t, func() bool {
		current, getErr := store.Get("scrub-vs-clean")
		if getErr != nil || current.Runtime().State != StateFailed {
			return false
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()

		reg := rt.lanes["scrub-vs-clean"]
		reg.retryAt = time.Time{}
		rt.lanes["scrub-vs-clean"] = reg

		return reg.state == laneExited
	}, "failed lane exit")

	rt.reconcile(ctx)
	waitFor(t, func() bool { return starts.Load() == 2 }, "failed lane retry")
	if scrubs.Load() == 0 || cleans.Load() != 0 {
		t.Fatalf("retry used scrubs=%d cleans=%d, want scrub only", scrubs.Load(), cleans.Load())
	}

	if err := store.Delete("scrub-vs-clean"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	rt.stopAndWait("scrub-vs-clean")
	rt.clean("scrub-vs-clean")
	if cleans.Load() == 0 {
		t.Fatal("deletion did not run the final cleanup")
	}

	rt.stopAll()
	rt.wg.Wait()
}

// TestUpdateKeepsEventLoopReactiveWhileDraining: a definition update must not
// park the event loop on the old lane's teardown — other sessions' events keep
// flowing while the replacement waits out the drain in its own goroutine.
func TestUpdateKeepsEventLoopReactiveWhileDraining(t *testing.T) {
	t.Parallel()

	store := NewStore()
	release := make(chan struct{})

	var otherStarted, slowReplacement atomic.Int32

	starter := func(ctx context.Context, s *Session, running func()) error {
		switch {
		case s.Slug == "other":
			otherStarted.Add(1)
		case s.Slug == "slow" && len(s.Langs) > 1:
			slowReplacement.Add(1)
		}
		running()
		<-ctx.Done()
		if s.Slug == "slow" {
			<-release // teardown held: the drain the loop must not wait for
		}

		return ctx.Err()
	}
	rt := NewRuntime(store, starter, nil, nil, slog.New(slog.DiscardHandler))
	createTestSessions(t, store, "slow")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	waitFor(t, func() bool {
		s, err := store.Get("slow")

		return err == nil && s.Runtime().State == StateRunning
	}, "initial lane runs")

	if _, err := store.UpdateLangs("slow", []core.Lang{"fr", "en"}, nil); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	if err := store.Create(testSession("other")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	// The proof: "other" starts while "slow" is still draining.
	waitFor(t, func() bool { return otherStarted.Load() == 1 }, "other lane starts during drain")
	if slowReplacement.Load() != 0 {
		t.Fatal("replacement started before its predecessor drained")
	}

	close(release)
	waitFor(t, func() bool { return slowReplacement.Load() == 1 }, "replacement starts after drain")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

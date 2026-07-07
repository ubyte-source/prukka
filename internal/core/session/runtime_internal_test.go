package session

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// countingStarter records starts per slug and blocks lanes until canceled.
type countingStarter struct {
	starts map[string]int
	mu     sync.Mutex
}

func (c *countingStarter) starter(ctx context.Context, s *Session) error {
	c.mu.Lock()
	c.starts[s.Slug]++
	c.mu.Unlock()

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
	rt := NewRuntime(store, starts.starter, slog.New(slog.DiscardHandler))

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
	rt := NewRuntime(store, func(context.Context, *Session) error {
		mu.Lock()
		runs++
		mu.Unlock()

		return nil // the source ended on its own
	}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := store.Create(testSession("ended")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	rt.launch(ctx, testSession("ended"))
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

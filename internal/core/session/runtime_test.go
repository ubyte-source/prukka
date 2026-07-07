package session_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// laneLog records lane starts and blocks lanes until their context ends.
type laneLog struct {
	live   map[string]int
	starts []string
	mu     sync.Mutex
}

func newLaneLog() *laneLog {
	return &laneLog{live: map[string]int{}}
}

// starter implements session.LaneStarter.
func (l *laneLog) starter(ctx context.Context, s *session.Session) error {
	l.mu.Lock()
	l.starts = append(l.starts, s.Slug+"/"+string(s.Langs[0]))
	l.live[s.Slug]++
	l.mu.Unlock()

	<-ctx.Done()

	l.mu.Lock()
	l.live[s.Slug]--
	l.mu.Unlock()

	return ctx.Err()
}

// snapshot returns copies of the counters.
func (l *laneLog) snapshot() (starts []string, live map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	starts = append(starts, l.starts...)

	live = make(map[string]int, len(l.live))
	for k, v := range l.live {
		live[k] = v
	}

	return starts, live
}

// eventually polls until check passes or the deadline hits.
func eventually(t *testing.T, check func() bool, what string) {
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

func TestRuntimeLifecycle(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	lanes := newLaneLog()

	// A session existing before Run must launch too.
	if err := store.Create(demo("pre")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	rt := session.NewRuntime(store, lanes.starter, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	eventually(t, func() bool { _, live := lanes.snapshot(); return live["pre"] == 1 }, "pre-existing lane")

	if err := store.Create(demo("live")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	eventually(t, func() bool { _, live := lanes.snapshot(); return live["live"] == 1 }, "created lane")

	updateAndDelete(t, store, lanes)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if _, live := lanes.snapshot(); live["pre"] != 0 {
		t.Fatal("pre-existing lane still running after shutdown")
	}
}

// updateAndDelete drives the restart-on-update and stop-on-delete phases.
func updateAndDelete(t *testing.T, store *session.Store, lanes *laneLog) {
	t.Helper()

	if _, err := store.UpdateLangs("live", []core.Lang{"fr"}, []core.Lang{"it"}); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}

	eventually(t, func() bool {
		starts, live := lanes.snapshot()

		return live["live"] == 1 && len(starts) == 3 && starts[2] == "live/en"
	}, "restarted lane")

	if err := store.Delete("live"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	eventually(t, func() bool { _, live := lanes.snapshot(); return live["live"] == 0 }, "deleted lane stop")
}

// failingStarter returns immediately with an error.
func failingStarter(context.Context, *session.Session) error {
	return errors.New("no provider key")
}

// overlapProbe is a starter whose teardown is slow, exposing any restart
// that launches the replacement before its predecessor finished.
type overlapProbe struct {
	mu      sync.Mutex
	live    int
	overlap bool
	starts  int
}

func (p *overlapProbe) starter(ctx context.Context, _ *session.Session) error {
	p.mu.Lock()
	p.live++
	p.starts++

	if p.live > 1 {
		p.overlap = true
	}
	p.mu.Unlock()

	<-ctx.Done()

	// A slow teardown: the window in which a hasty replacement would
	// overlap this lane's cleanup.
	time.Sleep(50 * time.Millisecond)

	p.mu.Lock()
	p.live--
	p.mu.Unlock()

	return ctx.Err()
}

func (p *overlapProbe) snapshot() (starts int, overlap bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.starts, p.overlap
}

// TestRuntimeRestartWaitsForTeardown: a replacement must not start while
// the old lane is still tearing down.
func TestRuntimeRestartWaitsForTeardown(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	probe := &overlapProbe{}

	ctx, cancel := context.WithCancel(t.Context())

	rt := session.NewRuntime(store, probe.starter, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("swap")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	eventually(t, func() bool { starts, _ := probe.snapshot(); return starts == 1 }, "first lane")

	if _, err := store.UpdateLangs("swap", []core.Lang{"fr"}, nil); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}

	eventually(t, func() bool { starts, _ := probe.snapshot(); return starts == 2 }, "restarted lane")

	if _, overlap := probe.snapshot(); overlap {
		t.Fatal("replacement lane started while its predecessor was still tearing down")
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRuntimeSurvivesFailingLanes(t *testing.T) {
	t.Parallel()

	store := session.NewStore()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rt := session.NewRuntime(store, failingStarter, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("degraded")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	// The session stays as metadata even though its lane failed.
	eventually(t, func() bool { return store.Count() == 1 }, "session survives failed lane")

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

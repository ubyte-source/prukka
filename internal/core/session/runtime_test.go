package session_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
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

type lockedBuffer struct {
	bytes.Buffer

	mu sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Buffer.String()
}

func newLaneLog() *laneLog {
	return &laneLog{live: map[string]int{}}
}

// starter implements session.LaneStarter.
func (l *laneLog) starter(ctx context.Context, s *session.Session, running func()) error {
	l.mu.Lock()
	l.starts = append(l.starts, s.Slug+"/"+string(s.Langs[0]))
	l.live[s.Slug]++
	l.mu.Unlock()
	running()

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
	maps.Copy(live, l.live)

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

	rt := session.NewRuntime(store, lanes.starter, nil, slog.New(slog.DiscardHandler))

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
func failingStarter(context.Context, *session.Session, func()) error {
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

func (p *overlapProbe) starter(ctx context.Context, _ *session.Session, running func()) error {
	p.mu.Lock()
	p.live++
	p.starts++

	if p.live > 1 {
		p.overlap = true
	}
	p.mu.Unlock()
	running()

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

	rt := session.NewRuntime(store, probe.starter, nil, slog.New(slog.DiscardHandler))

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

func TestRuntimeShutdownCleansEveryStoredSession(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	lanes := newLaneLog()
	cleaned := make(chan string, 1)
	rt := session.NewRuntime(store, lanes.starter, func(slug string) { cleaned <- slug },
		slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("shutdown-clean")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	eventually(t, func() bool {
		_, live := lanes.snapshot()

		return live["shutdown-clean"] == 1
	}, "live lane")
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	select {
	case slug := <-cleaned:
		if slug != "shutdown-clean" {
			t.Fatalf("cleaned %q, want shutdown-clean", slug)
		}
	case <-time.After(time.Second):
		t.Fatal("stored session outputs were not cleaned on shutdown")
	}
}

// TestRuntimeDeleteCleansSelfEndedLane: a lane that finished on its own has
// no teardown left, so deletion itself must drop the session's outputs.
func TestRuntimeDeleteCleansSelfEndedLane(t *testing.T) {
	t.Parallel()

	store := session.NewStore()

	var ran atomic.Bool

	finished := func(context.Context, *session.Session, func()) error {
		ran.Store(true)

		return nil
	}

	var (
		mu      sync.Mutex
		cleaned []string
	)

	cleanup := func(slug string) {
		mu.Lock()
		cleaned = append(cleaned, slug)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rt := session.NewRuntime(store, finished, cleanup, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("ended")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	// The lane having run proves the event stream is live, so the delete
	// event below cannot be lost to the pre-subscription window.
	eventually(t, ran.Load, "self-ending lane run")

	if err := store.Delete("ended"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(cleaned) == 1 && cleaned[0] == "ended"
	}, "cleanup of the self-ended lane's outputs")

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

	rt := session.NewRuntime(store, failingStarter, nil, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("degraded")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	// The definition stays registered and its observed failure is explicit.
	eventually(t, func() bool {
		stored, err := store.Get("degraded")

		return err == nil && stored.Runtime().State == session.StateFailed &&
			stored.Runtime().Error == "no provider key"
	}, "failed runtime state")

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRuntimeLogDoesNotExposeFailureSecrets(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	starter := func(context.Context, *session.Session, func()) error {
		return errors.New("open /Users/alice/private.wav: denied; token=stream-secret")
	}

	ctx, cancel := context.WithCancel(t.Context())
	rt := session.NewRuntime(store, starter, nil, logger)
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	if err := store.Create(demo("safe-log")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	eventually(t, func() bool {
		stored, err := store.Get("safe-log")

		return err == nil && stored.Runtime().State == session.StateFailed &&
			strings.Contains(logs.String(), "lane unavailable")
	}, "failed status and log")
	for _, secret := range []string{"/Users/alice", "private.wav", "stream-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("runtime log exposes %q: %s", secret, logs.String())
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRuntimeReportsRunningThenNaturalFinish(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	release := make(chan struct{})
	starter := func(_ context.Context, _ *session.Session, running func()) error {
		running()
		<-release

		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	rt := session.NewRuntime(store, starter, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("finite")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	eventually(t, func() bool {
		stored, err := store.Get("finite")

		return err == nil && stored.Runtime().State == session.StateRunning
	}, "running state")
	close(release)
	eventually(t, func() bool {
		stored, err := store.Get("finite")

		return err == nil && stored.Runtime().State == session.StateFinished
	}, "natural finish state")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestStatusEventsDoNotRestartTheLane(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	lanes := newLaneLog()
	ctx, cancel := context.WithCancel(t.Context())
	rt := session.NewRuntime(store, lanes.starter, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("stable")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	eventually(t, func() bool {
		stored, err := store.Get("stable")

		return err == nil && stored.Runtime().State == session.StateRunning
	}, "running status event")
	time.Sleep(50 * time.Millisecond)
	starts, _ := lanes.snapshot()
	if len(starts) != 1 {
		t.Fatalf("status event started %d lanes, want one", len(starts))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReplacedLaneCannotPublishLateRunningState(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	callbacks := make(chan func(), 2)
	allowReplacement := make(chan struct{})
	var starts atomic.Int32
	starter := func(ctx context.Context, _ *session.Session, running func()) error {
		call := starts.Add(1)
		callbacks <- running
		if call == 2 {
			<-allowReplacement
			running()
		}
		<-ctx.Done()

		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	rt := session.NewRuntime(store, starter, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("replace-status")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	oldRunning := <-callbacks
	if _, err := store.UpdateLangs("replace-status", []core.Lang{"fr"}, nil); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	<-callbacks // the replacement is started but intentionally not ready
	oldRunning()

	stored, err := store.Get("replace-status")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got := stored.Runtime().State; got != session.StateStarting {
		t.Fatalf("late predecessor changed replacement to %q, want starting", got)
	}
	close(allowReplacement)
	eventually(t, func() bool {
		current, getErr := store.Get("replace-status")

		return getErr == nil && current.Runtime().State == session.StateRunning
	}, "replacement running state")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

package session_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/procio"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// laneLog records lane starts and blocks lanes until their context ends.
type laneLog struct {
	live   map[string]int
	starts []string
	mu     sync.Mutex
}

// dropLog records the slugs whose outputs were dropped.
type dropLog struct {
	dropped []string
	mu      sync.Mutex
}

func (d *dropLog) Drop(slug string) {
	d.mu.Lock()
	d.dropped = append(d.dropped, slug)
	d.mu.Unlock()
}

func (d *dropLog) Scrub(string) {}

func (d *dropLog) slugs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.dropped)
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
	testkit.Eventually(t, 2*time.Second, check, what)
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

	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: lanes.starter, Log: slog.New(slog.DiscardHandler)})

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

func failingStarter(context.Context, *session.Session, func()) error {
	return errors.New("no provider key")
}

// overlapProbe is a starter whose teardown is slow.
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

func TestRuntimeRestartWaitsForTeardown(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	probe := &overlapProbe{}

	ctx, cancel := context.WithCancel(t.Context())

	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: probe.starter, Log: slog.New(slog.DiscardHandler)})

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
	dropped := &dropLog{}
	rt := session.NewRuntime(&session.RuntimeDeps{
		Store:   store,
		Start:   lanes.starter,
		Outputs: dropped,
		Log:     slog.New(slog.DiscardHandler),
	})

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
	// Run returns only after cleanStored, so the drops are already recorded.
	if got := dropped.slugs(); len(got) != 1 || got[0] != "shutdown-clean" {
		t.Fatalf("dropped %v on shutdown, want [shutdown-clean]", got)
	}
}

func TestRuntimeDeleteCleansSelfEndedLane(t *testing.T) {
	t.Parallel()

	store := session.NewStore()

	var ran atomic.Bool

	finished := func(context.Context, *session.Session, func()) error {
		ran.Store(true)

		return nil
	}

	dropped := &dropLog{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rt := session.NewRuntime(&session.RuntimeDeps{
		Store:   store,
		Start:   finished,
		Outputs: dropped,
		Log:     slog.New(slog.DiscardHandler),
	})

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
		got := dropped.slugs()

		return len(got) == 1 && got[0] == "ended"
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

	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: failingStarter, Log: slog.New(slog.DiscardHandler)})

	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := store.Create(demo("degraded")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

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
	// The two shapes producers declare: a typed path error from the stdlib and a
	// child's own stderr, each naming what it did not author.
	starter := func(context.Context, *session.Session, func()) error {
		return fmt.Errorf("file ingress: %w", procio.WithStderr(
			&os.PathError{Op: "open", Path: "/Users/alice/private.wav", Err: errors.New("denied")},
			"loading token=stream-secret failed",
		))
	}

	ctx, cancel := context.WithCancel(t.Context())
	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: starter, Log: logger})
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
	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: starter, Log: slog.New(slog.DiscardHandler)})
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

// TestStatusEventsDoNotRestartTheLane asserts quiescence: synctest.Wait
// returns once every other goroutine is durably blocked, which a sleep can
// only approximate.
func TestStatusEventsDoNotRestartTheLane(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := session.NewStore()
		lanes := newLaneLog()
		ctx, cancel := context.WithCancel(t.Context())
		rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: lanes.starter, Log: slog.New(slog.DiscardHandler)})
		done := make(chan error, 1)
		go func() { done <- rt.Run(ctx) }()

		if err := store.Create(demo("stable")); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		synctest.Wait()

		if stored, err := store.Get("stable"); err != nil || stored.Runtime().State != session.StateRunning {
			t.Fatalf("settled state = (%v, %v), want running", stored.Runtime().State, err)
		}
		if starts, _ := lanes.snapshot(); len(starts) != 1 {
			t.Fatalf("status event started %d lanes, want one", len(starts))
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	})
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
	rt := session.NewRuntime(&session.RuntimeDeps{Store: store, Start: starter, Log: slog.New(slog.DiscardHandler)})
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

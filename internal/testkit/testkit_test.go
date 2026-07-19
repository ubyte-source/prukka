package testkit_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/testkit"
)

func TestEventuallyReturnsOnceTheConditionHolds(t *testing.T) {
	t.Parallel()

	var flips atomic.Int32
	testkit.Eventually(t, time.Second, func() bool {
		return flips.Add(1) >= 3
	}, "counter reached three")

	if flips.Load() < 3 {
		t.Fatalf("condition returned after %d polls, want at least 3", flips.Load())
	}
}

// recordingTB captures Fatalf instead of failing the real test, exiting the
// goroutine the way testing.T does.
type recordingTB struct {
	testing.TB

	failed atomic.Bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(string, ...any) {
	r.failed.Store(true)
	runtime.Goexit()
}

func TestEventuallyFailsAfterTheDeadline(t *testing.T) {
	t.Parallel()

	rec := &recordingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		testkit.Eventually(rec, 20*time.Millisecond, func() bool { return false }, "never true")
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Eventually did not return")
	}
	if !rec.failed.Load() {
		t.Fatal("Eventually returned without failing on a never-true condition")
	}
}

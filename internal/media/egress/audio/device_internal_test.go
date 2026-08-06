package audio

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// deviceStallBudget is short enough to sever within a test's patience, long
// enough that a loaded runner does not sever a healthy write.
const deviceStallBudget = 30 * time.Millisecond

func TestStallGuardSeversAWedgedWrite(t *testing.T) {
	t.Parallel()

	wedged := newBlockingSink(0)
	guard := newStallGuard(wedged, deviceStallBudget)
	t.Cleanup(func() {
		if err := guard.Close(); err != nil {
			t.Errorf("close stall guard: %v", err)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := guard.Write(make([]byte, 4))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("severed write returned nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stall guard never severed the wedged write")
	}
}

// racedCloser counts how often the guarded writer was actually closed.
type racedCloser struct {
	err    error
	closes atomic.Int64
}

func (c *racedCloser) Write(p []byte) (int, error) { return len(p), nil }

func (c *racedCloser) Close() error {
	c.closes.Add(1)

	return c.err
}

func TestStallGuardCloseRunsOnceAndCachesItsFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("device sink close failed")
	sink := &racedCloser{err: want}
	// An hour of stall budget keeps the watcher out of this test: the only
	// closers are the concurrent callers below.
	guard := newStallGuard(sink, time.Hour)

	const closers = 8
	results := make(chan error, closers)
	start := make(chan struct{})
	for range closers {
		go func() {
			<-start
			results <- guard.Close()
		}()
	}
	close(start)
	for range closers {
		if err := <-results; !errors.Is(err, want) {
			t.Fatalf("concurrent Close = %v, want the cached %v", err, want)
		}
	}
	if got := sink.closes.Load(); got != 1 {
		t.Fatalf("underlying closes = %d, want exactly 1", got)
	}
}

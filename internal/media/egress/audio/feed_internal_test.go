package audio

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFeedClosesItsWriterExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &countingCloser{}
	f := &feeder{out: w, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkEncoder}
	if err := f.run(ctx); err != nil {
		t.Fatalf("feed on an ended context = %v, want nil", err)
	}

	if w.closes != 1 {
		t.Fatalf("writer closed %d times, want exactly once", w.closes)
	}
}

func TestFeedReportsCloseFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &feeder{out: &failingCloser{}, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkEncoder}
	err := f.run(ctx)
	if err == nil || !strings.Contains(err.Error(), "close encoder feed") {
		t.Fatalf("feed = %v, want the close failure surfaced", err)
	}
}

func TestFeedCancellationClosesBlockedWriterExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	sink := newCloseUnblocksWriter()
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	done := make(chan error, 1)
	f := &feeder{out: sink, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkDevice}
	go func() { done <- f.ticks(ctx, ticks) }()

	<-sink.writeStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("feeder.ticks = %v, want the unblocked writer error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("feed cancellation did not close and unblock the writer")
	}
	if got := sink.closeCount(); got != 1 {
		t.Fatalf("writer closed %d times, want exactly once", got)
	}
}

// errFeedSinkClose is the failure the shared close carries back.
var errFeedSinkClose = errors.New("feed sink close failed")

// slowFailingCloser stays inside Close long enough for the cancellation
// callback and the feed's exit path to overlap in one underlying close.
type slowFailingCloser struct {
	closing chan struct{}
	release chan struct{}
	once    sync.Once
	closes  atomic.Int64
}

func (*slowFailingCloser) Write(p []byte) (int, error) { return len(p), nil }

func (c *slowFailingCloser) Close() error {
	c.closes.Add(1)
	c.once.Do(func() { close(c.closing) })
	<-c.release

	return errFeedSinkClose
}

func TestFeedCancellationAndExitShareOneCloseResult(t *testing.T) {
	t.Parallel()

	sink := &slowFailingCloser{closing: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	f := &feeder{out: sink, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkDevice}

	done := make(chan error, 1)
	go func() { done <- f.ticks(ctx, make(chan time.Time)) }()
	cancel()

	select {
	case <-sink.closing:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation never reached the sink close")
	}
	close(sink.release)

	if err := <-done; !errors.Is(err, errFeedSinkClose) {
		t.Fatalf("feeder.ticks = %v, want the cached %v", err, errFeedSinkClose)
	}
	if got := sink.closes.Load(); got != 1 {
		t.Fatalf("sink closed %d times, want exactly once", got)
	}
}

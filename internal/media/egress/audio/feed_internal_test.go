package audio

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestFeedClosesItsWriterExactlyOnce: a second close would re-reap the
// encoder process.
func TestFeedClosesItsWriterExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &countingCloser{}
	if err := feed(ctx, w, idleMixer(), false, defaultFeedConfig()); err != nil {
		t.Fatalf("feed on an ended context = %v, want nil", err)
	}

	if w.closes != 1 {
		t.Fatalf("writer closed %d times, want exactly once", w.closes)
	}
}

// TestFeedReportsCloseFailure: a failed drain must surface, not vanish.
func TestFeedReportsCloseFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := feed(ctx, &failingCloser{}, idleMixer(), false, defaultFeedConfig())
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
	go func() {
		done <- feedTicks(ctx, sink, idleMixer(), true, defaultFeedConfig(), ticks)
	}()

	<-sink.writeStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("feedTicks = %v, want the unblocked writer error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("feed cancellation did not close and unblock the writer")
	}
	if got := sink.closeCount(); got != 1 {
		t.Fatalf("writer closed %d times, want exactly once", got)
	}
}

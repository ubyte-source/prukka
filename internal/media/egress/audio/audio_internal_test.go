package audio

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// countingCloser records how often the encoder feed closes its writer.
type countingCloser struct{ closes int }

func (*countingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *countingCloser) Close() error              { c.closes++; return nil }

// failingCloser reports a close failure.
type failingCloser struct{ countingCloser }

func (f *failingCloser) Close() error {
	f.closes++

	return errors.New("boom")
}

func idleMixer() *pipeline.Mixer {
	return pipeline.NewMixer(pipeline.NewTrack(), pipeline.NewTrack(), -15)
}

// TestFeedClosesItsWriterExactlyOnce: a second close would re-reap the
// encoder process.
func TestFeedClosesItsWriterExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &countingCloser{}
	if err := feed(ctx, w, idleMixer()); err != nil {
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

	err := feed(ctx, &failingCloser{}, idleMixer())
	if err == nil || !strings.Contains(err.Error(), "close encoder feed") {
		t.Fatalf("feed = %v, want the close failure surfaced", err)
	}
}

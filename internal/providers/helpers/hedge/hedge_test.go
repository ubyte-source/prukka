package hedge_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/hedge"
)

// pacedSTT answers call n after delays[n] (last entry repeats), unless err
// is set. It counts calls and honors cancellation.
type pacedSTT struct {
	err    error
	delays []time.Duration
	calls  atomic.Int32
}

func (p *pacedSTT) Transcribe(ctx context.Context, _ *core.Utterance, _ core.Lang) (core.Transcript, error) {
	n := int(p.calls.Add(1)) - 1
	if n >= len(p.delays) {
		n = len(p.delays) - 1
	}

	select {
	case <-time.After(p.delays[n]):
	case <-ctx.Done():
		return core.Transcript{}, ctx.Err()
	}

	if p.err != nil {
		return core.Transcript{}, p.err
	}

	return core.Transcript{Text: "ok"}, nil
}

// warm feeds enough fast calls to establish a p95.
func warm(t *testing.T, s *hedge.STT) {
	t.Helper()

	for range 8 {
		if _, err := s.Transcribe(t.Context(), &core.Utterance{}, "it"); err != nil {
			t.Fatalf("warm-up call: %v", err)
		}
	}
}

func TestNoHedgeBeforeWarmup(t *testing.T) {
	t.Parallel()

	inner := &pacedSTT{delays: []time.Duration{50 * time.Millisecond}}
	s := hedge.NewSTT(inner, time.Millisecond)

	if _, err := s.Transcribe(t.Context(), &core.Utterance{}, "it"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (no p95 yet, no hedge)", got)
	}
}

func TestFastCallNeverHedges(t *testing.T) {
	t.Parallel()

	inner := &pacedSTT{delays: []time.Duration{5 * time.Millisecond}}
	s := hedge.NewSTT(inner, 100*time.Millisecond)

	warm(t, s)
	inner.calls.Store(0)

	if _, err := s.Transcribe(t.Context(), &core.Utterance{}, "it"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (answer beat the floor)", got)
	}
}

func TestSlowCallFiresBackupAndBackupWins(t *testing.T) {
	t.Parallel()

	// Warm with 5 ms calls, then one call stalls: the backup (fast again)
	// must win well before the stalled primary.
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	inner := &pacedSTT{delays: []time.Duration{
		ms(5), ms(5), ms(5), ms(5), ms(5), ms(5), ms(5), ms(5), // warm-up
		ms(2000), // stalled primary
		ms(5),    // backup
	}}

	s := hedge.NewSTT(inner, 30*time.Millisecond)
	warm(t, s)

	start := time.Now()

	got, err := s.Transcribe(t.Context(), &core.Utterance{}, "it")
	if err != nil || got.Text != "ok" {
		t.Fatalf("Transcribe = %q, %v", got.Text, err)
	}

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("hedged call took %v; the backup never won", elapsed)
	}

	if calls := inner.calls.Load(); calls != 10 {
		t.Fatalf("calls = %d, want 10 (8 warm + primary + backup)", calls)
	}
}

func TestErrorBeforeHedgeReturnsWithoutBackup(t *testing.T) {
	t.Parallel()

	inner := &pacedSTT{err: errors.New("stt down"), delays: []time.Duration{time.Millisecond}}
	s := hedge.NewSTT(inner, time.Hour)

	if _, err := s.Transcribe(t.Context(), &core.Utterance{}, "it"); err == nil {
		t.Fatal("Transcribe swallowed the inner error")
	}

	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (retries live below the hedge)", got)
	}
}

func TestBothRacersErroringReportsTheFirst(t *testing.T) {
	t.Parallel()

	// Warm with successes, then flip to errors slower than the floor so
	// both racers run and both fail.
	inner := &pacedSTT{delays: []time.Duration{5 * time.Millisecond}}
	s := hedge.NewSTT(inner, 10*time.Millisecond)
	warm(t, s)

	inner.err = errors.New("stt down")
	inner.delays = []time.Duration{50 * time.Millisecond}
	inner.calls.Store(0)

	if _, err := s.Transcribe(t.Context(), &core.Utterance{}, "it"); err == nil {
		t.Fatal("Transcribe swallowed both errors")
	}

	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want primary + backup", got)
	}
}

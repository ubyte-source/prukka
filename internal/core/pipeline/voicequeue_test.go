package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// quantumSamples is the sink drain window used throughout: 20 ms at the
// reference rate.
const quantumSamples = 20 * core.SampleRate / 1000

func durationForN(n int) time.Duration {
	return time.Duration(n) * time.Second / time.Duration(core.SampleRate)
}

func TestVoiceQueueDrainsTakesBackToBack(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(time.Second)
	cursor := q.Cursor()
	q.Append(0, tone(11, quantumSamples))
	q.Append(0, tone(22, quantumSamples/2))

	dst := make([]int16, quantumSamples)
	got := make([]int16, 0, quantumSamples*2)
	for range 3 {
		pcm, status := cursor.NextInto(dst)
		if status != pipeline.PullReady {
			break
		}
		got = append(got, pcm.Data...)
	}

	// First quantum is all take 11; the second half-quantum is take 22 then
	// zero-padded to a full window.
	if got[0] != 11 || got[quantumSamples-1] != 11 {
		t.Fatalf("first window not take 11: %d..%d", got[0], got[quantumSamples-1])
	}
	if got[quantumSamples] != 22 || got[quantumSamples+quantumSamples/2-1] != 22 {
		t.Fatalf("second window did not start with take 22")
	}
	if tail := got[quantumSamples+quantumSamples/2]; tail != 0 {
		t.Fatalf("sub-quantum tail not zero-filled: %d", tail)
	}
	if _, status := cursor.NextInto(dst); status != pipeline.PullPending {
		t.Fatalf("drained queue should be PullPending, got %v", status)
	}
}

func TestVoiceQueueUnderrunReportsPending(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(time.Second)
	cursor := q.Cursor()
	dst := make([]int16, quantumSamples)
	if _, status := cursor.NextInto(dst); status != pipeline.PullPending {
		t.Fatalf("empty queue should be PullPending, got %v", status)
	}
}

func TestVoiceQueueFinishedAndDrainedReportsEOF(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(time.Second)
	cursor := q.Cursor()
	q.Append(0, tone(7, quantumSamples))
	dst := make([]int16, quantumSamples)

	if _, status := cursor.NextInto(dst); status != pipeline.PullReady {
		t.Fatalf("expected the take to drain")
	}
	if _, status := cursor.NextInto(dst); status != pipeline.PullPending {
		t.Fatalf("unfinished empty queue is pending, not EOF")
	}
	q.Finish()
	if _, status := cursor.NextInto(dst); status != pipeline.PullEOF {
		t.Fatalf("finished drained queue should be PullEOF, got %v", status)
	}
}

func TestVoiceQueueDropsStalestOverCap(t *testing.T) {
	t.Parallel()

	// Cap the backlog at one quantum; append three, so only the newest survives.
	q := pipeline.NewVoiceQueue(durationForN(quantumSamples))
	cursor := q.Cursor()
	q.Append(0, tone(1, quantumSamples)) // stalest
	q.Append(0, tone(2, quantumSamples))
	q.Append(0, tone(3, quantumSamples)) // newest

	dst := make([]int16, quantumSamples)
	pcm, status := cursor.NextInto(dst)
	if status != pipeline.PullReady {
		t.Fatalf("expected a window, got %v", status)
	}
	if pcm.Data[0] != 3 {
		t.Fatalf("newest take should survive the cap, got mark %d", pcm.Data[0])
	}
	// The clock advanced across the two dropped quanta, so PTS is not zero.
	if pcm.PTS != durationForN(2*quantumSamples) {
		t.Fatalf("PTS should account for dropped span: got %v want %v",
			pcm.PTS, durationForN(2*quantumSamples))
	}
	if _, status := cursor.NextInto(dst); status != pipeline.PullPending {
		t.Fatalf("only one capped window should remain, got %v", status)
	}
}

func TestVoiceQueuePTSIsMonotonic(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(time.Second)
	cursor := q.Cursor()
	q.Append(0, tone(5, quantumSamples*3))

	dst := make([]int16, quantumSamples)
	last := time.Duration(-1)
	for range 3 {
		pcm, status := cursor.NextInto(dst)
		if status != pipeline.PullReady {
			t.Fatalf("expected ready window")
		}
		if pcm.PTS <= last {
			t.Fatalf("PTS not monotonic: %v after %v", pcm.PTS, last)
		}
		last = pcm.PTS
	}
}

// BenchmarkFrameVoiceQueueNextInto carries the ^BenchmarkFrame prefix so the
// CI bench gate holds the call playout path to 0 allocs/op, like the mixer's.
func BenchmarkFrameVoiceQueueNextInto(b *testing.B) {
	q := pipeline.NewVoiceQueue(0)
	cursor := q.Cursor()
	dst := make([]int16, quantumSamples)
	chunk := tone(9, quantumSamples)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		q.Append(0, chunk)
		if _, status := cursor.NextInto(dst); status != pipeline.PullReady {
			b.Fatalf("expected ready window")
		}
	}
}

// TestVoiceQueueConsumerSuccession: a released cursor is spent, but the
// TEMPLATE keeps admitting fresh cursors — a replaced device push starts a
// new job and pulls a new cursor, and refusing it would fail every re-push
// with "not ready" forever.
func TestVoiceQueueConsumerSuccession(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(0)
	first := q.Cursor()

	if !first.BeginPlayout() {
		t.Fatal("first consumer refused")
	}
	if !first.BeginPlayout() {
		t.Fatal("BeginPlayout must stay idempotent for the live consumer")
	}
	first.ReleasePlayout()
	if first.BeginPlayout() {
		t.Fatal("a released cursor must stay spent, like a Mixer cursor")
	}

	successor := q.Cursor()
	if !successor.BeginPlayout() {
		t.Fatal("successor cursor refused after release — re-push would fail forever")
	}
	q.Append(0, make([]int16, quantumSamples))
	dst := make([]int16, quantumSamples)
	if _, status := successor.NextInto(dst); status != pipeline.PullReady {
		t.Fatalf("successor pull = %v, want ready", status)
	}
	successor.ReleasePlayout()
}

// TestVoiceQueueCursorsAreIndependentReadHeads: concurrent sinks each hear
// the FULL feed. One shared head would hand alternate windows to whichever
// sink pulled first — both streams unintelligible.
func TestVoiceQueueCursorsAreIndependentReadHeads(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(time.Second)
	device, monitor := q.Cursor(), q.Cursor()
	if !device.BeginPlayout() || !monitor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}

	q.Append(0, tone(11, quantumSamples))
	q.Append(0, tone(22, quantumSamples))

	for _, cursor := range []pipeline.Playout{device, monitor} {
		dst := make([]int16, quantumSamples)
		for _, want := range []int16{11, 22} {
			pcm, status := cursor.NextInto(dst)
			if status != pipeline.PullReady || pcm.Data[0] != want {
				t.Fatalf("cursor window = (%v, mark %d), want ready take %d", status, pcm.Data[0], want)
			}
		}
	}
	device.ReleasePlayout()
	monitor.ReleasePlayout()
}

// TestVoiceQueueSealedRefusesSuccessor: WaitPlayout seals the consumer set;
// a successor arriving after the seal must be refused.
func TestVoiceQueueSealedRefusesSuccessor(t *testing.T) {
	t.Parallel()

	q := pipeline.NewVoiceQueue(0)
	cursor := q.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("first consumer refused")
	}
	cursor.ReleasePlayout()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.WaitPlayout(ctx); err != nil {
		t.Fatalf("WaitPlayout: %v", err)
	}

	if q.Cursor().BeginPlayout() {
		t.Fatal("sealed playout accepted a successor")
	}
}

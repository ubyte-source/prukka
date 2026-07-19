package pipeline_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// tone fills n samples with a constant marker value.
func tone(marker int16, n int) []int16 {
	return slices.Repeat([]int16{marker}, n)
}

func TestTrackPlacesGapsAsSilence(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// One second of speech at 8 s, then one at 10 s: the gap must be
	// silence, timed on the source clock.
	track.Append(8*time.Second, tone(1, core.SampleRate))
	track.Append(10*time.Second, tone(2, core.SampleRate))

	start, ok := track.Start()
	if !ok || start != 8*time.Second {
		t.Fatalf("Start = %v (ok=%v), want the first segment's schedule", start, ok)
	}

	got := make([]int16, 3*core.SampleRate)
	track.Window(start, got)

	if got[0] != 1 || got[core.SampleRate] != 0 || got[2*core.SampleRate] != 2 {
		t.Fatalf("layout wrong: [0]=%d [1s]=%d [2s]=%d, want 1,0,2",
			got[0], got[core.SampleRate], got[2*core.SampleRate])
	}
}

// TestTrackTrimsToLiveWindow: memory bounded, origin slides, recent audio
// survives, ancient reads silence.
func TestTrackTrimsToLiveWindow(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// Ten minutes of continuous audio, appended second by second.
	for s := range 600 {
		track.Append(time.Duration(s)*time.Second, tone(int16(s%100+1), core.SampleRate))
	}

	start, ok := track.Start()
	if !ok {
		t.Fatal("track lost its anchor")
	}

	if start < 9*time.Minute {
		t.Fatalf("Start = %v; ten minutes of live audio were retained (unbounded growth)", start)
	}

	// The newest second must still be intact at its own instant.
	got := make([]int16, core.SampleRate)
	track.Window(599*time.Second, got)

	if got[0] != int16(599%100+1) {
		t.Fatalf("recent audio lost by trim: got %d", got[0])
	}

	// Trimmed history reads as silence, not garbage.
	track.Window(0, got)

	if got[0] != 0 {
		t.Fatalf("trimmed span reads %d, want silence", got[0])
	}
}

// TestTrackTrimResumesAfterLastConsumerLeaves: the playout fence lives only
// as long as a cursor. A listener who rendered a window and left must not pin
// every later take in memory for the rest of the session.
func TestTrackTrimResumesAfterLastConsumerLeaves(t *testing.T) {
	t.Parallel()

	voice := pipeline.NewTrack()
	bed := pipeline.NewTrack()
	for s := range 30 {
		at := time.Duration(s) * time.Second
		bed.Append(at, tone(1, core.SampleRate))
		voice.Append(at, tone(int16(s+1), core.SampleRate))
	}

	m := pipeline.NewMixer(bed, voice, -15)
	cursor := m.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}
	// One rendered window arms the voice fence near the timeline start.
	window := make([]int16, pipeline.SamplesInQuantum(100*time.Millisecond))
	if _, status := cursor.NextInto(window); status != pipeline.PullReady {
		t.Fatalf("cursor pull = %v, want ready", status)
	}
	cursor.ReleasePlayout()

	// The listener is gone; minutes of further takes must trim to retention.
	for s := 30; s < 300; s++ {
		voice.Append(time.Duration(s)*time.Second, tone(int16(s%100+1), core.SampleRate))
	}
	start, ok := voice.Start()
	if !ok || start == 0 {
		t.Fatalf("Start = (%v, %v); the departed consumer's fence still pins the track", start, ok)
	}
}

func TestTrackSpillsInsteadOfOverwriting(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// Two seconds placed at 0; the next segment is scheduled at 1 s but
	// must spill to 2 s (never overwrite placed speech).
	track.Append(0, tone(1, 2*core.SampleRate))

	placed := track.Append(time.Second, tone(2, core.SampleRate))
	if placed != 2*time.Second {
		t.Fatalf("placed at %v, want the spill to 2s", placed)
	}

	got := make([]int16, 3*core.SampleRate)
	track.Window(0, got)

	if got[2*core.SampleRate] != 2 {
		t.Fatal("spilled segment did not land after the first")
	}
}

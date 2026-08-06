package pipeline_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func tone(marker int16, n int) []int16 {
	return slices.Repeat([]int16{marker}, n)
}

func TestTrackPlacesGapsAsSilence(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

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

func TestTrackTrimsToLiveWindow(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

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

	got := make([]int16, core.SampleRate)
	track.Window(599*time.Second, got)

	if got[0] != int16(599%100+1) {
		t.Fatalf("recent audio lost by trim: got %d", got[0])
	}

	track.Window(0, got)

	if got[0] != 0 {
		t.Fatalf("trimmed span reads %d, want silence", got[0])
	}
}

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
	window := make([]int16, pipeline.SamplesInQuantum(100*time.Millisecond))
	if _, status := cursor.NextInto(window); status != pipeline.PullReady {
		t.Fatalf("cursor pull = %v, want ready", status)
	}
	cursor.ReleasePlayout()

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

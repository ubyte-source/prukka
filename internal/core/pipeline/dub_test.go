package pipeline_test

import (
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestTempoForClampsToReadableRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		source, take time.Duration
		want         float64
	}{
		{source: 2 * time.Second, take: 2 * time.Second, want: 1.0},
		{source: 2 * time.Second, take: 3 * time.Second, want: 1.20}, // too long → cap
		{source: 2 * time.Second, take: time.Second, want: 0.85},     // too short → floor
		{source: 2 * time.Second, take: 2200 * time.Millisecond, want: 1.1},
		{source: 0, take: time.Second, want: 1.0},
	}

	for _, tc := range cases {
		if got := pipeline.TempoFor(tc.source, tc.take); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Fatalf("TempoFor(%v, %v) = %v, want %v", tc.source, tc.take, got, tc.want)
		}
	}
}

// TestPitchForClampsToNaturalRange: ±4 semitones max, missing fundamentals
// leave the take untouched.
func TestPitchForClampsToNaturalRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		speaker, take, want float64
	}{
		{speaker: 200, take: 200, want: 1.0},
		{speaker: 180, take: 200, want: 0.9},  // deeper speaker → shift down
		{speaker: 220, take: 200, want: 1.1},  // brighter speaker → shift up
		{speaker: 100, take: 200, want: 0.80}, // too far down → floor
		{speaker: 400, take: 200, want: 1.25}, // too far up → cap
		{speaker: 0, take: 200, want: 1.0},    // unvoiced speaker
		{speaker: 200, take: 0, want: 1.0},    // unanalyzable take
	}

	for _, tc := range cases {
		if got := pipeline.PitchFor(tc.speaker, tc.take); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Fatalf("PitchFor(%v, %v) = %v, want %v", tc.speaker, tc.take, got, tc.want)
		}
	}
}

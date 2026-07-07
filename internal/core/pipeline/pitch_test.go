package pipeline_test

import (
	"math"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// sine synthesizes one second of a pure tone at the reference rate.
func sine(hz float64, amplitude int16) []int16 {
	samples := make([]int16, pipeline.SampleRate)
	for i := range samples {
		samples[i] = int16(float64(amplitude) *
			math.Sin(2*math.Pi*hz*float64(i)/pipeline.SampleRate))
	}

	return samples
}

func TestMedianF0RecoversKnownPitches(t *testing.T) {
	t.Parallel()

	// A deep male, a mid and a high female register.
	for _, hz := range []float64{110, 200, 320} {
		got := pipeline.MedianF0(sine(hz, 8000), pipeline.SampleRate)

		if math.Abs(got-hz)/hz > 0.05 {
			t.Errorf("MedianF0(%v Hz tone) = %.1f, want within 5%%", hz, got)
		}
	}
}

func TestMedianF0RejectsSilenceAndShortInput(t *testing.T) {
	t.Parallel()

	if got := pipeline.MedianF0(make([]int16, pipeline.SampleRate), pipeline.SampleRate); got != 0 {
		t.Fatalf("silence pitched at %.1f Hz, want 0", got)
	}

	if got := pipeline.MedianF0(sine(200, 8000)[:100], pipeline.SampleRate); got != 0 {
		t.Fatalf("sub-frame input pitched at %.1f Hz, want 0", got)
	}

	if got := pipeline.MedianF0(sine(200, 8000), 0); got != 0 {
		t.Fatalf("zero rate pitched at %.1f Hz, want 0", got)
	}
}

func TestMedianF0IgnoresQuietNoiseFloor(t *testing.T) {
	t.Parallel()

	// Deterministic sub-threshold "noise": far below the voiced RMS gate.
	samples := make([]int16, pipeline.SampleRate)
	for i := range samples {
		samples[i] = int16((i%7 - 3) * 20)
	}

	if got := pipeline.MedianF0(samples, pipeline.SampleRate); got != 0 {
		t.Fatalf("noise floor pitched at %.1f Hz, want 0", got)
	}
}

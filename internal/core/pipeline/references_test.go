package pipeline_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// clip builds a mono 16 kHz PCM of the given whole-second length.
func clip(seconds int, fill int16) core.PCM {
	data := make([]int16, seconds*16000)
	for i := range data {
		data[i] = fill
	}

	return core.PCM{Data: data, Rate: 16000, Ch: 1}
}

// TestReferenceCaptureAdoptsFirstLongEnoughClip: the first clip past the
// minimum becomes the stable reference.
func TestReferenceCaptureAdoptsFirstLongEnoughClip(t *testing.T) {
	t.Parallel()

	r := pipeline.NewReferences()

	// Too short: no reference yet.
	if ref := r.Capture(0, clip(1, 100)); len(ref) != 0 {
		t.Fatalf("a 1 s clip was adopted (%d samples)", len(ref))
	}

	// Long enough: adopted.
	adopted := r.Capture(0, clip(3, 200))
	if len(adopted) != 3*16000 || adopted[0] != 200 {
		t.Fatalf("reference not adopted: %d samples, first=%d", len(adopted), first(adopted))
	}

	// Later clips reuse the first reference — the timbre must stay stable.
	again := r.Capture(0, clip(5, 50))
	if len(again) != len(adopted) || again[0] != 200 {
		t.Fatalf("reference changed on a later utterance: first=%d", first(again))
	}
}

// TestReferenceCaptureCopies: the reference must survive the pipeline
// reusing the utterance's pooled buffer.
func TestReferenceCaptureCopies(t *testing.T) {
	t.Parallel()

	r := pipeline.NewReferences()
	audio := clip(3, 100)

	ref := r.Capture(1, audio)

	// Simulate the pool overwriting the utterance buffer in place.
	for i := range audio.Data {
		audio.Data[i] = -1
	}

	if ref[0] != 100 {
		t.Fatalf("reference aliased the pooled buffer: ref[0]=%d", ref[0])
	}
}

// TestReferenceCaptureIsPerSpeaker: each speaker gets its own reference.
func TestReferenceCaptureIsPerSpeaker(t *testing.T) {
	t.Parallel()

	r := pipeline.NewReferences()

	a := r.Capture(0, clip(3, 10))
	b := r.Capture(1, clip(3, 20))

	if a[0] == b[0] {
		t.Fatal("two speakers share one reference")
	}
}

// first returns a sample for error messages, or -1 when empty.
func first(samples []int16) int16 {
	if len(samples) == 0 {
		return -1
	}

	return samples[0]
}

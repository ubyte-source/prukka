package pipeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// ramp fills a chunk with a deterministic sample counter so tests can verify
// no sample is lost or duplicated across chunk boundaries.
func ramp(start, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16((start + i) % 1000)
	}

	return out
}

func TestFramerRejectsWrongFormat(t *testing.T) {
	t.Parallel()

	f := pipeline.NewFramer()

	err := f.Push(core.PCM{Data: make([]int16, 10), Rate: 44100, Ch: 2}, func(core.PCM) {})
	if !errors.Is(err, pipeline.ErrBadFormat) {
		t.Fatalf("Push error = %v, want ErrBadFormat", err)
	}
}

func TestFramerReframesAcrossChunks(t *testing.T) {
	t.Parallel()

	f := pipeline.NewFramer()

	var (
		frames  int
		samples []int16
	)

	emit := func(p core.PCM) {
		if len(p.Data) != pipeline.FrameSamples {
			t.Fatalf("frame %d has %d samples, want %d", frames, len(p.Data), pipeline.FrameSamples)
		}

		if want := time.Duration(frames) * pipeline.FrameDuration; p.PTS != want {
			t.Fatalf("frame %d PTS = %v, want %v", frames, p.PTS, want)
		}

		// Frames alias reused storage: copy to inspect afterwards.
		samples = append(samples, p.Data...)
		frames++
	}

	// Odd chunk sizes force partial-frame carry between pushes.
	total := 0
	for _, n := range []int{100, 300, 517, 1024, 63, 2000} {
		chunk := core.PCM{
			Data: ramp(total, n),
			Rate: pipeline.SampleRate,
			Ch:   1,
			PTS:  time.Duration(total) * time.Second / pipeline.SampleRate,
		}
		if err := f.Push(chunk, emit); err != nil {
			t.Fatalf("Push returned error: %v", err)
		}

		total += n
	}

	if want := total / pipeline.FrameSamples; frames != want {
		t.Fatalf("emitted %d frames, want %d", frames, want)
	}

	for i, s := range samples {
		if s != int16(i%1000) {
			t.Fatalf("sample %d = %d, want %d (lost or duplicated audio)", i, s, i%1000)
		}
	}
}

func BenchmarkFramerPush(b *testing.B) {
	f := pipeline.NewFramer()
	chunk := core.PCM{Data: make([]int16, 1024), Rate: pipeline.SampleRate, Ch: 1}
	emit := func(core.PCM) {}

	b.ReportAllocs()

	for b.Loop() {
		if err := f.Push(chunk, emit); err != nil {
			b.Fatal(err)
		}
	}
}

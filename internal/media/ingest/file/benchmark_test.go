package file

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func BenchmarkFrameFileNext(b *testing.B) {
	quantumSamples := pipeline.SamplesInQuantum(DefaultPCMQuantum)
	input, err := os.CreateTemp(b.TempDir(), "pcm-*.raw")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := input.Write(make([]byte, quantumSamples*2)); err != nil {
		b.Fatal(err)
	}

	// The benchmark measures the read+decode path, so the pacer origin is
	// backdated by the whole run's quanta — Next must never block on the wall
	// clock, at any b.N. This ties the file to the `for range b.N` idiom:
	// under b.Loop, b.N is not meaningful before the loop.
	frames := &frames{
		input: input, dataBytes: int64(quantumSamples * 2), loop: true,
		start: time.Now().Add(-time.Duration(b.N+1) * DefaultPCMQuantum), raw: make([]byte, quantumSamples*2),
		samples: make([]int16, quantumSamples),
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		frame, nextErr := frames.Next(ctx)
		if nextErr != nil || len(frame.Data) != quantumSamples {
			b.Fatalf("Next = %d samples, %v", len(frame.Data), nextErr)
		}
	}

	b.StopTimer()
	if err := input.Close(); err != nil {
		b.Fatal(err)
	}
}

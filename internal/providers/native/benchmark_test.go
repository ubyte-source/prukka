package native

import (
	"context"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func BenchmarkFrameNativeSTTPush(b *testing.B) {
	frame := core.PCM{
		Data: make([]int16, core.SampleRate/10),
		Rate: core.SampleRate,
		Ch:   1,
	}
	session := &sttSession{
		spawnedHelper: &spawnedHelper{stdin: discardWriteCloser{}},
		rate:          core.SampleRate,
	}
	ctx := context.Background()
	if err := session.Push(ctx, frame); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(frame.Data) * 2))
	b.ResetTimer()

	for b.Loop() {
		// Advance PTS by the frame's own duration, as every ingress does: the
		// timeline then coalesces into one span, the steady state measured here.
		frame.PTS += time.Duration(len(frame.Data)) * time.Second / core.SampleRate
		if err := session.Push(ctx, frame); err != nil {
			b.Fatal(err)
		}
	}

	if spans := len(session.timeline.frames); spans != 1 {
		b.Fatalf("timeline spans = %d, want 1: the benchmark drifted off the coalescing steady state", spans)
	}
}

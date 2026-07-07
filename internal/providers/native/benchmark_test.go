package native

import (
	"context"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func BenchmarkFrameNativeSTTPush(b *testing.B) {
	frame := core.PCM{
		Data: make([]int16, pipeline.SampleRate/10),
		Rate: pipeline.SampleRate,
		Ch:   1,
	}
	session := &sttSession{
		ctx:   context.Background(),
		stdin: discardWriteCloser{},
		rate:  pipeline.SampleRate,
	}
	if err := session.Push(frame); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(frame.Data) * 2))
	b.ResetTimer()

	for b.Loop() {
		if err := session.Push(frame); err != nil {
			b.Fatal(err)
		}
	}
}

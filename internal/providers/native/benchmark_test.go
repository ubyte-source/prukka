package native

import (
	"context"
	"testing"

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
		ctx:           context.Background(),
		rate:          core.SampleRate,
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

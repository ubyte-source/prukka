package pipeline

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

const benchmarkFrameSamples = core.SampleRate / 10

var (
	benchmarkByte   byte
	benchmarkSample int16
)

func newBenchmarkCursor(b *testing.B) *mixCursor {
	b.Helper()

	bed := NewTrack()
	voice := NewTrack()
	bedSamples := make([]int16, benchmarkFrameSamples)
	voiceSamples := make([]int16, benchmarkFrameSamples)
	for i := range benchmarkFrameSamples {
		bedSamples[i] = 1_000
		voiceSamples[i] = 2_000 // above the sidechain's speaking threshold
	}
	bed.Append(0, bedSamples)
	voice.Append(0, voiceSamples)

	cursor, ok := NewMixer(bed, voice, -15).Cursor().(*mixCursor)
	if !ok {
		b.Fatal("Mixer.Cursor did not return the package's own render head")
	}

	return cursor
}

func BenchmarkFrameMixEncode(b *testing.B) {
	cursor := newBenchmarkCursor(b)
	samples := make([]int16, benchmarkFrameSamples)
	payload := make([]byte, 0, benchmarkFrameSamples*2)
	if _, ok := pullInto(cursor, samples); !ok {
		b.Fatal("mixer is not ready")
	}

	b.ReportAllocs()
	b.SetBytes(benchmarkFrameSamples * 2)
	b.ResetTimer()

	for b.Loop() {
		// Rewind onto the one-frame fixture: measure the sidechain, not silence.
		cursor.clock = 0
		pcm, ok := pullInto(cursor, samples)
		if !ok {
			b.Fatal("mixer is not ready")
		}

		payload = AppendS16LE(payload[:0], pcm.Data)
		benchmarkByte = payload[0]
	}
}

func BenchmarkFrameMixerPullInto(b *testing.B) {
	cursor := newBenchmarkCursor(b)
	samples := make([]int16, benchmarkFrameSamples)
	if _, ok := pullInto(cursor, samples); !ok {
		b.Fatal("mixer is not ready")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		cursor.clock = 0
		pcm, ok := pullInto(cursor, samples)
		if !ok {
			b.Fatal("mixer is not ready")
		}
		benchmarkSample = pcm.Data[0]
	}
}

func BenchmarkFrameAppendS16LE(b *testing.B) {
	samples := make([]int16, benchmarkFrameSamples)
	payload := make([]byte, 0, benchmarkFrameSamples*2)

	b.ReportAllocs()
	b.SetBytes(benchmarkFrameSamples * 2)
	b.ResetTimer()

	for b.Loop() {
		payload = AppendS16LE(payload[:0], samples)
		benchmarkByte = payload[0]
	}
}

func BenchmarkFrameDecode(b *testing.B) {
	payload := make([]byte, benchmarkFrameSamples*2)
	samples := make([]int16, benchmarkFrameSamples)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for b.Loop() {
		DecodeS16LE(samples, payload)
		// Sink one element, or the inlined stores are dead and this loop is empty.
		benchmarkSample = samples[0]
	}
}

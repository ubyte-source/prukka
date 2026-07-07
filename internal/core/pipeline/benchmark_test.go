package pipeline

import "testing"

const benchmarkFrameSamples = SampleRate / 10

var (
	benchmarkByte   byte
	benchmarkSample int16
)

func newBenchmarkMixer() *Mixer {
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

	return NewMixer(bed, voice, -15)
}

func BenchmarkFrameMixEncode(b *testing.B) {
	mixer := newBenchmarkMixer()
	samples := make([]int16, benchmarkFrameSamples)
	payload := make([]byte, 0, benchmarkFrameSamples*2)
	if _, ok := mixer.PullInto(samples); !ok {
		b.Fatal("mixer is not ready")
	}

	b.ReportAllocs()
	b.SetBytes(benchmarkFrameSamples * 2)
	b.ResetTimer()

	for b.Loop() {
		// Reuse the populated window so the benchmark measures the active
		// sidechain path instead of silence beyond the one-frame fixture.
		mixer.clock = 0
		pcm, ok := mixer.PullInto(samples)
		if !ok {
			b.Fatal("mixer is not ready")
		}

		payload = AppendS16LE(payload[:0], pcm.Data)
		benchmarkByte = payload[0]
	}
}

func BenchmarkFrameMixerPullInto(b *testing.B) {
	mixer := newBenchmarkMixer()
	samples := make([]int16, benchmarkFrameSamples)
	if _, ok := mixer.PullInto(samples); !ok {
		b.Fatal("mixer is not ready")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		mixer.clock = 0
		pcm, ok := mixer.PullInto(samples)
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

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for b.Loop() {
		DecodeS16LE(samples, payload)
	}
}

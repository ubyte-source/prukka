package wasapi

import (
	"math"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestOpenBufferConfiguration(t *testing.T) {
	t.Parallel()

	config := defaultOpenConfig()
	if config.bufferDuration != DefaultBufferDuration {
		t.Fatalf("default buffer = %v, want %v", config.bufferDuration, DefaultBufferDuration)
	}
	if got := referenceTime(config.bufferDuration); got != 2_000_000 {
		t.Fatalf("default REFERENCE_TIME = %d, want 2000000", got)
	}

	WithBufferDuration(40 * time.Millisecond)(&config)
	if config.bufferDuration != 40*time.Millisecond {
		t.Fatalf("call buffer = %v, want 40ms", config.bufferDuration)
	}
	if got := referenceTime(config.bufferDuration); got != 400_000 {
		t.Fatalf("call REFERENCE_TIME = %d, want 400000", got)
	}
	if got := referenceTime(referenceTimeUnit + time.Nanosecond); got != 2 {
		t.Fatalf("rounded REFERENCE_TIME = %d, want 2", got)
	}
}

func TestOpenBufferRejectsNonPositiveDuration(t *testing.T) {
	t.Parallel()

	for _, duration := range []time.Duration{0, -time.Millisecond} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("WithBufferDuration(%v) did not panic", duration)
				}
			}()

			_ = WithBufferDuration(duration)
		}()
	}
}

// s16 encodes samples as the engine's reference payload.
func s16(t *testing.T, samples ...int16) []byte {
	t.Helper()

	return pipeline.EncodeS16LE(samples)
}

func TestConvertUpsamplesAndDuplicatesChannels(t *testing.T) {
	t.Parallel()

	// 16 kHz → 48 kHz stereo: 3 output frames per input sample, both
	// channels identical, values interpolated between neighbors.
	got, err := convert(s16(t, 0, 32767), 48000, 2)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if len(got) != 2*3*2 {
		t.Fatalf("frames = %d floats, want 12", len(got))
	}

	for frame := range len(got) / 2 {
		if got[2*frame] != got[2*frame+1] {
			t.Fatalf("frame %d channels differ: %v vs %v", frame, got[2*frame], got[2*frame+1])
		}
	}

	if got[0] != 0 {
		t.Fatalf("first frame = %v, want 0", got[0])
	}

	// Second frame sits a third of the way toward full scale.
	want := float32(32767.0 / 32768.0 / 3.0)
	if math.Abs(float64(got[2]-want)) > 1e-4 {
		t.Fatalf("interpolated frame = %v, want ≈%v", got[2], want)
	}
}

func TestConvertPreservesSignedSamples(t *testing.T) {
	t.Parallel()

	got, err := convert(s16(t, -32768, -1, 0, 32767), sourceRate, 1)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	want := []float32{-1, -1.0 / 32768, 0, 32767.0 / 32768}
	if len(got) != len(want) {
		t.Fatalf("samples = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestConvertIntoReusesDestination(t *testing.T) {
	t.Parallel()

	storage := make([]float32, 12)
	got, err := convertInto(storage[:0], s16(t, 0, 32767), 48000, 2)
	if err != nil {
		t.Fatalf("convertInto: %v", err)
	}
	if &got[0] != &storage[0] {
		t.Fatal("convertInto replaced a destination with sufficient capacity")
	}

	empty, err := convertInto(storage[:0], nil, 48000, 2)
	if err != nil || len(empty) != 0 || cap(empty) == 0 {
		t.Fatalf("empty convertInto did not preserve reusable storage: len=%d err=%v", len(empty), err)
	}
	if &empty[:1][0] != &storage[0] {
		t.Fatal("empty convertInto replaced reusable storage")
	}
}

func TestConvertRejectsBadInput(t *testing.T) {
	t.Parallel()

	if _, err := convert([]byte{1}, 48000, 2); err == nil {
		t.Fatal("odd payload accepted")
	}

	if _, err := convert(s16(t, 1), 0, 2); err == nil {
		t.Fatal("zero rate accepted")
	}

	if got, err := convert(nil, 48000, 2); err != nil || got != nil {
		t.Fatalf("empty payload = %v, %v", got, err)
	}

	if _, err := convert(s16(t, 1), math.MaxInt, 2); err == nil {
		t.Fatal("oversized converted payload accepted")
	}
}

func BenchmarkConvertReferenceChunk(b *testing.B) {
	samples := make([]int16, sourceRate/10)
	payload := pipeline.EncodeS16LE(samples)

	b.ReportAllocs()
	for range b.N {
		if _, convertErr := convert(payload, 48000, 2); convertErr != nil {
			b.Fatalf("convert: %v", convertErr)
		}
	}
}

func BenchmarkFrameConvertReferenceChunk(b *testing.B) {
	samples := make([]int16, sourceRate/10)
	payload := pipeline.EncodeS16LE(samples)
	converted, err := convertInto(nil, payload, 48000, 2)
	if err != nil {
		b.Fatalf("convert: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		converted, err = convertInto(converted[:0], payload, 48000, 2)
		if err != nil {
			b.Fatalf("convert: %v", err)
		}
	}
}

package wasapi

import (
	"math"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// s16 encodes samples as the engine's reference payload.
func s16(t *testing.T, samples ...int16) []byte {
	t.Helper()

	out, err := pipeline.EncodeS16LE(samples)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	return out
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
}

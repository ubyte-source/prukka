package pipeline_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestS16LERoundTrip(t *testing.T) {
	t.Parallel()

	samples := []int16{0, 1, -1, 32767, -32768, 12345}

	encoded, err := pipeline.EncodeS16LE(samples)
	if err != nil {
		t.Fatalf("EncodeS16LE returned error: %v", err)
	}

	if len(encoded) != len(samples)*2 {
		t.Fatalf("encoded %d bytes, want %d", len(encoded), len(samples)*2)
	}

	decoded := make([]int16, len(samples))

	n, err := pipeline.DecodeS16LE(decoded, encoded)
	if err != nil {
		t.Fatalf("DecodeS16LE returned error: %v", err)
	}

	if n != len(samples) {
		t.Fatalf("decoded %d samples, want %d", n, len(samples))
	}

	for i, want := range samples {
		if decoded[i] != want {
			t.Fatalf("sample %d = %d, want %d", i, decoded[i], want)
		}
	}
}

// TestDecodeS16LETruncates: odd trailing bytes drop and short destinations
// take what fits, never an error.
func TestDecodeS16LETruncates(t *testing.T) {
	t.Parallel()

	dst := make([]int16, 4)

	n, err := pipeline.DecodeS16LE(dst, []byte{1, 0, 2})
	if err != nil || n != 1 || dst[0] != 1 {
		t.Fatalf("odd payload: n=%d dst[0]=%d err=%v, want 1 sample", n, dst[0], err)
	}

	short := make([]int16, 1)

	n, err = pipeline.DecodeS16LE(short, []byte{1, 0, 2, 0})
	if err != nil || n != 1 || short[0] != 1 {
		t.Fatalf("short dst: n=%d short[0]=%d err=%v, want 1 sample", n, short[0], err)
	}

	n, err = pipeline.DecodeS16LE(nil, []byte{1, 0})
	if err != nil || n != 0 {
		t.Fatalf("nil dst: n=%d err=%v, want 0, nil", n, err)
	}
}

package pipeline_test

import (
	"slices"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestS16LERoundTrip(t *testing.T) {
	t.Parallel()

	samples := []int16{0, 1, -1, 32767, -32768, 12345}

	encoded := pipeline.EncodeS16LE(samples)

	if len(encoded) != len(samples)*2 {
		t.Fatalf("encoded %d bytes, want %d", len(encoded), len(samples)*2)
	}

	decoded := make([]int16, len(samples))

	n := pipeline.DecodeS16LE(decoded, encoded)

	if n != len(samples) {
		t.Fatalf("decoded %d samples, want %d", n, len(samples))
	}

	for i, want := range samples {
		if decoded[i] != want {
			t.Fatalf("sample %d = %d, want %d", i, decoded[i], want)
		}
	}
}

func TestAppendS16LEReusesDestination(t *testing.T) {
	t.Parallel()

	dst := make([]byte, 1, 7)
	dst[0] = 0xaa
	got := pipeline.AppendS16LE(dst, []int16{1, -1, -32768})
	want := []byte{0xaa, 0x01, 0x00, 0xff, 0xff, 0x00, 0x80}

	if &got[0] != &dst[0] {
		t.Fatal("AppendS16LE replaced a destination with sufficient capacity")
	}
	if !slices.Equal(got, want) {
		t.Fatalf("AppendS16LE = %v, want %v", got, want)
	}

	tight := []byte{0xbb}
	grown := pipeline.AppendS16LE(tight, []int16{256})
	if !slices.Equal(grown, []byte{0xbb, 0x00, 0x01}) {
		t.Fatalf("growing AppendS16LE = %v, want [187 0 1]", grown)
	}
}

// TestDecodeS16LETruncates: odd trailing bytes drop and short destinations
// take what fits, never an error.
func TestDecodeS16LETruncates(t *testing.T) {
	t.Parallel()

	dst := make([]int16, 4)

	n := pipeline.DecodeS16LE(dst, []byte{1, 0, 2})
	if n != 1 || dst[0] != 1 {
		t.Fatalf("odd payload: n=%d dst[0]=%d, want 1 sample", n, dst[0])
	}

	short := make([]int16, 1)

	n = pipeline.DecodeS16LE(short, []byte{1, 0, 2, 0})
	if n != 1 || short[0] != 1 {
		t.Fatalf("short dst: n=%d short[0]=%d, want 1 sample", n, short[0])
	}

	n = pipeline.DecodeS16LE(nil, []byte{1, 0})
	if n != 0 {
		t.Fatalf("nil dst: n=%d, want 0", n)
	}
}

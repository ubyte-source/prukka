package engine

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func TestDecodePCM16WAV(t *testing.T) {
	t.Parallel()

	want := []int16{-32768, -1, 0, 1, 32767}
	got, rate, err := decodePCM16WAV(encodeWAV(want, 22050))
	if err != nil || rate != 22050 || !reflect.DeepEqual(got, want) {
		t.Fatalf("decodePCM16WAV = %v, %d, %v", got, rate, err)
	}

	truncated := encodeWAV(want, 22050)[:44]
	if _, _, err := decodePCM16WAV(truncated); !errors.Is(err, errIncompleteWAV) {
		t.Fatalf("truncated error = %v, want incomplete", err)
	}

	invalid := encodeWAV(want, 22050)
	binary.LittleEndian.PutUint16(invalid[22:24], 2)
	if _, _, err := decodePCM16WAV(invalid); err == nil {
		t.Fatal("stereo WAV succeeded")
	}
}

func TestResampleLinear(t *testing.T) {
	t.Parallel()

	in := []int16{0, 1000, 2000, 3000}
	if got := resampleLinear(in, 16000, 16000); &got[0] != &in[0] {
		t.Fatal("same-rate resample copied its input")
	}
	got := resampleLinear(in, 4, 8)
	want := []int16{0, 500, 1000, 1500, 2000, 2500, 3000, 3000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resample = %v, want %v", got, want)
	}
}

func TestPCMConversionRoundTrip(t *testing.T) {
	t.Parallel()

	want := []int16{-32768, -1, 0, 1, 32767}
	if got := bytesToInt16(int16ToBytes(want)); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
}

func TestEncodeWAVHeader(t *testing.T) {
	t.Parallel()

	pcm := []int16{1, -1, 2}
	wav := encodeWAV(pcm, 16000)
	if len(wav) != 44+len(pcm)*2 {
		t.Fatalf("wav length = %d", len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("invalid WAV markers: %q/%q/%q", wav[:4], wav[8:12], wav[36:40])
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != 6 {
		t.Fatalf("data length = %d", got)
	}
}

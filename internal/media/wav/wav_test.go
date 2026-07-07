package wav_test

import (
	"encoding/binary"
	"testing"

	"github.com/ubyte-source/prukka/internal/media/wav"
)

// header is the RIFF/WAVE header length the encoder emits.
const header = 44

// TestEncodeHeader pins the RIFF header downstream decoders read: magic
// strings, PCM format, and the sizes that make the payload seekable.
func TestEncodeHeader(t *testing.T) {
	t.Parallel()

	samples := []int16{1, -1, 2, -2}

	out, err := wav.Encode(samples, 16000, 1)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}

	if string(out[0:4]) != "RIFF" || string(out[8:12]) != "WAVE" || string(out[12:16]) != "fmt " {
		t.Fatalf("malformed header: %q", out[:16])
	}

	le := binary.LittleEndian
	if rate := le.Uint32(out[24:28]); rate != 16000 {
		t.Fatalf("sample rate %d, want 16000", rate)
	}

	if channels := le.Uint16(out[22:24]); channels != 1 {
		t.Fatalf("channels %d, want 1", channels)
	}

	dataBytes := len(samples) * 2
	if riffSize := le.Uint32(out[4:8]); int(riffSize) != len(out)-8 {
		t.Fatalf("RIFF size %d, want %d", riffSize, len(out)-8)
	}

	if len(out) != header+dataBytes {
		t.Fatalf("total %d bytes, want header %d + data %d", len(out), header, dataBytes)
	}
}

// TestEncodePayloadIsLittleEndian: bytes are checked directly to keep the
// sign reinterpretation out of the test.
func TestEncodePayloadIsLittleEndian(t *testing.T) {
	t.Parallel()

	out, err := wav.Encode([]int16{0x0102, -1}, 16000, 1)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}

	// 0x0102 little-endian is 0x02 0x01.
	if out[header] != 0x02 || out[header+1] != 0x01 {
		t.Fatalf("first sample bytes = %#x %#x, want 02 01", out[header], out[header+1])
	}

	// -1 is 0xFFFF in two's complement.
	if out[header+2] != 0xFF || out[header+3] != 0xFF {
		t.Fatalf("second sample bytes = %#x %#x, want ff ff", out[header+2], out[header+3])
	}
}

// TestEncodeRejectsOutOfRangeInput: empty audio and impossible formats must
// fail loudly, not produce a corrupt header.
func TestEncodeRejectsOutOfRangeInput(t *testing.T) {
	t.Parallel()

	if _, err := wav.Encode(nil, 16000, 1); err == nil {
		t.Fatal("empty audio encoded without error")
	}

	if _, err := wav.Encode([]int16{1}, 0, 1); err == nil {
		t.Fatal("zero rate encoded without error")
	}

	if _, err := wav.Encode([]int16{1}, 16000, 3); err == nil {
		t.Fatal("three channels encoded without error")
	}
}

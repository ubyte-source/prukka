package file

import (
	"encoding/binary"
	"testing"
)

// buildWAV assembles a minimal 16 kHz mono PCM RIFF file around the samples.
func buildWAV(samples []int16, bits uint16) []byte {
	const (
		rate     = uint32(16000)
		channels = uint16(1)
	)

	le := binary.LittleEndian
	data := make([]byte, len(samples)*2)

	for i, s := range samples {
		le.PutUint16(data[i*2:], uint16(int32(s)&0xFFFF)) // two's complement, lossless
	}

	fmtChunk := make([]byte, 16)
	le.PutUint16(fmtChunk[0:2], 1) // PCM
	le.PutUint16(fmtChunk[2:4], channels)
	le.PutUint32(fmtChunk[4:8], rate)
	le.PutUint32(fmtChunk[8:12], rate*uint32(channels)*uint32(bits)/8)
	le.PutUint16(fmtChunk[12:14], channels*bits/8)
	le.PutUint16(fmtChunk[14:16], bits)

	body := append([]byte("WAVE"), riffChunk("fmt ", fmtChunk)...)
	body = append(body, riffChunk("data", data)...)

	out := append([]byte("RIFF"), make([]byte, 4)...)
	le.PutUint32(out[4:8], uint32(len(body)&0x7FFFFFFF))

	return append(out, body...)
}

func riffChunk(id string, body []byte) []byte {
	out := make([]byte, 8+len(body))
	copy(out, id)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(body)&0x7FFFFFFF))
	copy(out[8:], body)

	return out
}

func TestParseWAVDecodesPCM(t *testing.T) {
	t.Parallel()

	want := []int16{0, 100, -100, 32767}

	got, err := parseWAV(buildWAV(want, 16))
	if err != nil {
		t.Fatalf("parseWAV returned error: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestParseWAVRejectsGarbage(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"not riff":   []byte("JUNKxxxxWAVE"),
		"empty":      {},
		"wrong bits": buildWAV([]int16{1}, 8),
	}

	for name, data := range cases {
		if _, err := parseWAV(data); err == nil {
			t.Fatalf("%s parsed without error", name)
		}
	}
}

func TestParseWAVRejectsTruncatedChunk(t *testing.T) {
	t.Parallel()

	wav := buildWAV([]int16{1, 2, 3}, 16)
	// Declare more data than the file carries.
	binary.LittleEndian.PutUint32(wav[len(wav)-8:], 4096)

	if _, err := parseWAV(wav); err == nil {
		t.Fatal("a truncated data chunk parsed without error")
	}
}

// FuzzParseWAV feeds arbitrary bytes to the WAV reader: it must return an
// error or samples, never panic, on any input a hostile file could hold.
func FuzzParseWAV(f *testing.F) {
	f.Add(buildWAV([]int16{1, -1, 100}, 16))
	f.Add([]byte("RIFF\x00\x00\x00\x00WAVE"))
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		if _, err := parseWAV(data); err != nil {
			return
		}
	})
}

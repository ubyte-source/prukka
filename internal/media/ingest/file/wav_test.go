package file

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// parseWAV decodes a complete WAV held in memory the way production reads it
// incrementally: inspectWAV for the layout, then the PCM payload.
func parseWAV(data []byte) ([]int16, error) {
	spec, err := inspectWAV(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	pcm := data[spec.dataOffset : spec.dataOffset+spec.dataBytes]
	out := make([]int16, len(pcm)/2)
	pipeline.DecodeS16LE(out, pcm)

	return out, nil
}

type countingReaderAt struct {
	reader io.ReaderAt

	bytesRead int
}

func (r *countingReaderAt) ReadAt(dst []byte, offset int64) (int, error) {
	n, err := r.reader.ReadAt(dst, offset)
	r.bytesRead += n

	return n, err
}

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

func TestInspectWAVDoesNotReadPCM(t *testing.T) {
	t.Parallel()

	wav := buildWAV(make([]int16, 32_000), 16)
	r := &countingReaderAt{reader: bytes.NewReader(wav)}

	spec, err := inspectWAV(r, int64(len(wav)))
	if err != nil {
		t.Fatalf("inspectWAV returned error: %v", err)
	}
	if spec.dataBytes != 64_000 {
		t.Fatalf("data size = %d, want 64000", spec.dataBytes)
	}
	if r.bytesRead >= len(wav) {
		t.Fatalf("inspection read %d of %d bytes, want metadata only", r.bytesRead, len(wav))
	}
}

func TestInspectWAVEnforcesStructuralBounds(t *testing.T) {
	t.Parallel()

	if _, err := inspectWAV(bytes.NewReader(nil), maxRIFFFileBytes+1); err == nil {
		t.Fatal("inspectWAV accepted a file beyond the RIFF size limit")
	}

	const chunkBytes = 8
	wav := make([]byte, 12+(maxRIFFChunks+1)*chunkBytes)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], 4+(maxRIFFChunks+1)*chunkBytes)
	copy(wav[8:12], "WAVE")
	for offset := 12; offset < len(wav); offset += chunkBytes {
		copy(wav[offset:offset+4], "JUNK")
	}

	_, err := inspectWAV(bytes.NewReader(wav), int64(len(wav)))
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("chunk-limit error = %v", err)
	}
}

func TestParseWAVRejectsNonProgressingData(t *testing.T) {
	t.Parallel()

	empty := buildWAV(nil, 16)
	if _, err := parseWAV(empty); err == nil || !strings.Contains(err.Error(), "empty data") {
		t.Fatalf("empty-data error = %v", err)
	}

	odd := buildWAV([]int16{1}, 16)
	binary.LittleEndian.PutUint32(odd[40:44], 1)
	if _, err := parseWAV(odd); err == nil || !strings.Contains(err.Error(), "unaligned") {
		t.Fatalf("odd-data error = %v", err)
	}
}

func TestParseWAVRejectsInconsistentPCMLayout(t *testing.T) {
	t.Parallel()

	wav := buildWAV([]int16{1}, 16)
	binary.LittleEndian.PutUint32(wav[28:32], 1)

	if _, err := parseWAV(wav); err == nil || !strings.Contains(err.Error(), "invalid PCM layout") {
		t.Fatalf("layout error = %v", err)
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

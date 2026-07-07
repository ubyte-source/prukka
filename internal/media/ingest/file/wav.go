package file

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// parseWAV decodes a 16 kHz mono 16-bit RIFF/WAVE file; anything else is
// rejected with a resampling hint.
func parseWAV(data []byte) ([]int16, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}

	var state wavState

	// Chunks are [id:4][size:4][body:size]; the 31-bit mask keeps the size
	// conversion provably in range.
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4:off+8]) & 0x7FFFFFFF)
		off += 8

		if off+size > len(data) {
			return nil, fmt.Errorf("truncated %q chunk", id)
		}

		if err := state.consume(id, data[off:off+size]); err != nil {
			return nil, err
		}

		off += size + size%2
	}

	if !state.formatSeen || state.samples == nil {
		return nil, errors.New("missing fmt or data chunk")
	}

	return state.samples, nil
}

// wavState accumulates the chunks that matter.
type wavState struct {
	samples    []int16
	formatSeen bool
}

// consume folds one chunk into the state.
func (s *wavState) consume(id string, body []byte) error {
	switch id {
	case "fmt ":
		if err := checkFormat(body); err != nil {
			return err
		}

		s.formatSeen = true
	case "data":
		s.samples = decodeSamples(body)
	}

	return nil
}

// checkFormat enforces the reference format (16 kHz mono).
func checkFormat(b []byte) error {
	if len(b) < 16 {
		return errors.New("short fmt chunk")
	}

	format := binary.LittleEndian.Uint16(b[0:2])
	channels := binary.LittleEndian.Uint16(b[2:4])
	rate := binary.LittleEndian.Uint32(b[4:8])
	bits := binary.LittleEndian.Uint16(b[14:16])

	if format != 1 || bits != 16 || channels != 1 || rate != pipeline.SampleRate {
		return fmt.Errorf(
			"need 16 kHz mono 16-bit PCM, got format=%d %d Hz × %d ch %d bit — "+
				"resample with: ffmpeg -i in -ar 16000 -ac 1 out.wav", format, rate, channels, bits)
	}

	return nil
}

// decodeSamples reads little-endian int16 samples through the shared
// decoder.
func decodeSamples(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	if _, err := pipeline.DecodeS16LE(out, b); err != nil {
		// Unreachable: the buffer is sized to the slice.
		return nil
	}

	return out
}

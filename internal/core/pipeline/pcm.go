package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// DecodeS16LE fills dst with little-endian 16-bit samples from src — the
// single decoder behind every PCM byte stream.
func DecodeS16LE(dst []int16, src []byte) (int, error) {
	n := min(len(src)/2, len(dst))
	if n == 0 {
		return 0, nil
	}

	if err := binary.Read(bytes.NewReader(src[:n*2]), binary.LittleEndian, dst[:n]); err != nil {
		return 0, fmt.Errorf("decode pcm: %w", err)
	}

	return n, nil
}

// EncodeS16LE renders samples as little-endian bytes — the write-side twin
// of DecodeS16LE.
func EncodeS16LE(samples []int16) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(samples) * 2)

	if err := binary.Write(&buf, binary.LittleEndian, samples); err != nil {
		return nil, fmt.Errorf("encode pcm: %w", err)
	}

	return buf.Bytes(), nil
}

// Package wav is the single RIFF/WAVE encoder the provider adapters share.
package wav

import (
	"encoding/binary"
	"fmt"
)

// headerSize is the RIFF/WAVE header length for plain PCM.
const headerSize = 44

// maxSamples bounds encodable audio far below uint32 overflow, keeping the
// size arithmetic provably safe.
const maxSamples = 10 * 60 * 16000

// Encode wraps PCM16 samples in a minimal RIFF/WAVE container.
func Encode(samples []int16, rate, channels int) ([]byte, error) {
	if len(samples) == 0 || len(samples) > maxSamples {
		return nil, fmt.Errorf("audio of %d samples is outside the encodable range", len(samples))
	}

	if rate <= 0 || rate > 384000 || channels <= 0 || channels > 2 {
		return nil, fmt.Errorf("unsupported wav format: %d Hz × %d ch", rate, channels)
	}

	dataBytes := len(samples) * 2
	buf := make([]byte, headerSize+dataBytes)

	le := binary.LittleEndian

	// The guards above bound every size below; the masks keep that bound
	// explicit for the overflow linter without changing a value.
	copy(buf[0:4], "RIFF")
	le.PutUint32(buf[4:8], uint32((headerSize-8+dataBytes)&0xFFFFFFFF))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	le.PutUint32(buf[16:20], 16) // PCM fmt chunk size
	le.PutUint16(buf[20:22], 1)  // audio format: PCM
	le.PutUint16(buf[22:24], uint16(channels&0xFFFF))
	le.PutUint32(buf[24:28], uint32(rate&0xFFFFFFFF))
	le.PutUint32(buf[28:32], uint32((rate*channels*2)&0xFFFFFFFF)) // byte rate
	le.PutUint16(buf[32:34], uint16((channels*2)&0xFFFF))          // block align
	le.PutUint16(buf[34:36], 16)                                   // bits per sample
	copy(buf[36:40], "data")
	le.PutUint32(buf[40:44], uint32(dataBytes&0xFFFFFFFF))

	for i, s := range samples {
		le.PutUint16(buf[headerSize+i*2:], uint16(int32(s)&0xFFFF))
	}

	return buf, nil
}

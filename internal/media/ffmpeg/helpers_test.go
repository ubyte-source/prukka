package ffmpeg_test

import (
	"encoding/binary"
	"math"
)

// tonePCM builds one second of 16 kHz mono s16le at 440 Hz.
func tonePCM() []byte {
	const n = 16000

	b := make([]byte, n*2)
	for i := range n {
		v := int32(8000*math.Sin(2*math.Pi*440*float64(i)/16000)) & 0xFFFF
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}

	return b
}

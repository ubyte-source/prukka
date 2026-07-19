package pipeline

import (
	"encoding/binary"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// SamplePeriod is the duration of one reference-rate sample.
const SamplePeriod = time.Second / core.SampleRate

// SamplesInQuantum converts a chunk duration into its reference-rate sample
// count. A quantum must be positive and sample-aligned; violations are
// programming errors at wiring time, so they panic (the trace names the
// caller).
func SamplesInQuantum(quantum time.Duration) int {
	if quantum <= 0 || quantum%SamplePeriod != 0 {
		panic("PCM quantum must be positive and sample-aligned")
	}

	samples64 := int64(quantum / SamplePeriod)
	samples := int(samples64)
	if int64(samples) != samples64 {
		panic("PCM quantum is too large")
	}

	return samples
}

// PeakS16 returns the absolute peak of signed 16-bit PCM. The int result can
// represent 32768, the magnitude of the minimum int16 value, without overflow.
// It is an allocation-free signal-presence primitive for telemetry; callers
// must not retain or expose the underlying audio.
func PeakS16(samples []int16) int {
	peak := 0
	for _, sample := range samples {
		magnitude := int(sample)
		if magnitude < 0 {
			magnitude = -magnitude
		}
		peak = max(peak, magnitude)
	}

	return peak
}

// DecodeS16LE fills dst with little-endian 16-bit samples from src — the
// single decoder behind every PCM byte stream.
func DecodeS16LE(dst []int16, src []byte) int {
	n := min(len(src)/2, len(dst))
	for i := range n {
		offset := i * 2
		dst[i] = int16(uint16(src[offset]) | uint16(src[offset+1])<<8)
	}

	return n
}

// EncodeS16LE renders samples as little-endian bytes — the write-side twin
// of DecodeS16LE.
func EncodeS16LE(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	encodeS16LE(out, samples)

	return out
}

// AppendS16LE appends samples as little-endian bytes to dst. Reusing dst's
// capacity keeps frame-path encoding allocation-free.
func AppendS16LE(dst []byte, samples []int16) []byte {
	start := len(dst)
	encodedLen := len(samples) * 2
	if encodedLen > cap(dst)-start {
		grown := make([]byte, start+encodedLen)
		copy(grown, dst)
		dst = grown
	} else {
		dst = dst[:start+encodedLen]
	}

	encodeS16LE(dst[start:], samples)

	return dst
}

func encodeS16LE(dst []byte, samples []int16) {
	// Four samples per 64-bit store: ~2.2x the byte-at-a-time loop on the
	// reference encode benchmark. binary.LittleEndian compiles to a single
	// unaligned store on the supported targets.
	i := 0
	for ; i+4 <= len(samples); i += 4 {
		packed := uint64(uint16(samples[i])) |
			uint64(uint16(samples[i+1]))<<16 |
			uint64(uint16(samples[i+2]))<<32 |
			uint64(uint16(samples[i+3]))<<48
		binary.LittleEndian.PutUint64(dst[i*2:], packed)
	}
	for ; i < len(samples); i++ {
		binary.LittleEndian.PutUint16(dst[i*2:], uint16(samples[i]))
	}
}

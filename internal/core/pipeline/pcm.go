package pipeline

// SampleRate is the pipeline's canonical audio sample rate in hertz: 16 kHz
// mono, the rate the speech engine expects and every stage assumes.
const SampleRate = 16000

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
	for i, sample := range samples {
		offset := i * 2
		value := uint16(sample)
		dst[offset] = byte(value)
		dst[offset+1] = byte(value >> 8)
	}
}

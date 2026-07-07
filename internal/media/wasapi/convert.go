// Package wasapi plays reference PCM into a Windows audio endpoint (ffmpeg
// ships no playback muxer there); conversion is portable, COM is not.
package wasapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// sourceRate is the engine's reference rate.
const sourceRate = 16000

// maxConvertedSamples caps one queued COM-thread buffer at 32 MiB.
const maxConvertedSamples = 8 << 20

// convert turns s16le mono at the reference rate into interleaved Float32
// frames at the endpoint's rate and channel count, linearly interpolated.
func convert(s16le []byte, outRate, outChannels int) ([]float32, error) {
	return convertInto(nil, s16le, outRate, outChannels)
}

// convertInto is convert with caller-owned storage. The returned slice aliases
// dst when its capacity is sufficient.
func convertInto(dst []float32, s16le []byte, outRate, outChannels int) ([]float32, error) {
	if len(s16le)%2 != 0 {
		return nil, fmt.Errorf("wasapi: odd s16le payload (%d bytes)", len(s16le))
	}

	if outRate <= 0 || outChannels <= 0 {
		return nil, fmt.Errorf("wasapi: endpoint format %d Hz × %d channels", outRate, outChannels)
	}

	samples := len(s16le) / 2
	if samples == 0 {
		return dst[:0], nil
	}

	if samples > math.MaxInt/outRate {
		return nil, errors.New("wasapi: converted frame count overflows int")
	}

	outFrames := samples * outRate / sourceRate
	if outFrames > maxConvertedSamples/outChannels {
		return nil, fmt.Errorf("wasapi: converted payload exceeds %d samples", maxConvertedSamples)
	}

	convertedSamples := outFrames * outChannels
	out := resize(dst, convertedSamples)

	for frame := range outFrames {
		pos := float64(frame) * sourceRate / float64(outRate)
		idx := int(pos)
		frac := float32(pos - float64(idx))

		value := sample(s16le, idx)
		if idx+1 < samples {
			value += (sample(s16le, idx+1) - value) * frac
		}

		for ch := range outChannels {
			out[frame*outChannels+ch] = value
		}
	}

	return out, nil
}

func resize(dst []float32, size int) []float32 {
	if cap(dst) < size {
		return make([]float32, size)
	}

	return dst[:size]
}

// sample decodes one little-endian PCM16 value directly from the caller's
// immutable input buffer.
func sample(s16le []byte, index int) float32 {
	offset := index * 2
	raw := binary.LittleEndian.Uint16(s16le[offset : offset+2])
	signed := int32(raw)
	if raw >= 1<<15 {
		signed -= 1 << 16
	}

	return float32(signed) / 32768
}

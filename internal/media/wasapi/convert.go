// Package wasapi plays reference PCM into a Windows audio endpoint (ffmpeg
// ships no playback muxer there); conversion is portable, COM is not.
package wasapi

import (
	"fmt"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// sourceRate is the engine's reference rate.
const sourceRate = 16000

// convert turns s16le mono at the reference rate into interleaved Float32
// frames at the endpoint's rate and channel count, linearly interpolated.
func convert(s16le []byte, outRate, outChannels int) ([]float32, error) {
	if len(s16le)%2 != 0 {
		return nil, fmt.Errorf("wasapi: odd s16le payload (%d bytes)", len(s16le))
	}

	if outRate <= 0 || outChannels <= 0 {
		return nil, fmt.Errorf("wasapi: endpoint format %d Hz × %d channels", outRate, outChannels)
	}

	decoded := make([]int16, len(s16le)/2)

	samples, err := pipeline.DecodeS16LE(decoded, s16le)
	if err != nil {
		return nil, err
	}

	if samples == 0 {
		return nil, nil
	}

	mono := make([]float32, samples)
	for i := range mono {
		mono[i] = float32(decoded[i]) / 32768
	}

	outFrames := samples * outRate / sourceRate
	out := make([]float32, outFrames*outChannels)

	for frame := range outFrames {
		pos := float64(frame) * sourceRate / float64(outRate)
		idx := int(pos)
		frac := float32(pos - float64(idx))

		value := mono[idx]
		if idx+1 < samples {
			value += (mono[idx+1] - mono[idx]) * frac
		}

		for ch := range outChannels {
			out[frame*outChannels+ch] = value
		}
	}

	return out, nil
}

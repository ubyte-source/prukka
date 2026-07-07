package pipeline

import "github.com/ubyte-source/prukka/internal/core"

// Resample converts mono PCM to outRate with linear interpolation — the
// in-process fast path when no time-stretch is needed.
func Resample(in core.PCM, outRate int) core.PCM {
	if in.Rate == outRate || len(in.Data) == 0 {
		out := in
		out.Rate = outRate

		return out
	}

	ratio := float64(in.Rate) / float64(outRate)
	n := int(float64(len(in.Data)) / ratio)
	data := make([]int16, n)

	last := len(in.Data) - 1

	for i := range data {
		src := float64(i) * ratio
		j := int(src)

		if j >= last {
			data[i] = in.Data[last]

			continue
		}

		frac := src - float64(j)
		data[i] = int16((1-frac)*float64(in.Data[j]) + frac*float64(in.Data[j+1]))
	}

	return core.PCM{Data: data, Rate: outRate, Ch: 1, PTS: in.PTS}
}

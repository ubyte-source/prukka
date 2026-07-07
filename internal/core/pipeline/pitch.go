package pipeline

import "sort"

// Human speaking range bounds the F0 search (Hz).
const (
	minF0 = 60
	maxF0 = 400
)

// Pitch analysis frames: 40 ms windows every 20 ms give the YIN difference
// function at least two full periods of the lowest voice at 16 kHz.
const (
	pitchFrame = SampleRate * 40 / 1000
	pitchHop   = SampleRate * 20 / 1000
)

// yinThreshold is the classic absolute-threshold step of the YIN paper:
// the first dip of the normalized difference below it is the period.
const yinThreshold = 0.15

// voicedRMS gates silent frames out of the estimate, as a fraction of full
// scale.
const voicedRMS = 0.008

// MedianF0 estimates the fundamental in Hz via YIN over voiced frames;
// 0 means none found.
func MedianF0(samples []int16, rate int) float64 {
	if rate <= 0 || len(samples) < pitchFrame {
		return 0
	}

	var voiced []float64

	for start := 0; start+pitchFrame <= len(samples); start += pitchHop {
		if f0 := frameF0(samples[start:start+pitchFrame], rate); f0 > 0 {
			voiced = append(voiced, f0)
		}
	}

	if len(voiced) == 0 {
		return 0
	}

	sort.Float64s(voiced)

	return voiced[len(voiced)/2]
}

// frameF0 runs YIN on one frame: difference function, cumulative mean
// normalization, absolute threshold. Returns 0 for unvoiced frames.
func frameF0(frame []int16, rate int) float64 {
	if rms(frame) < voicedRMS {
		return 0
	}

	minLag := rate / maxF0
	maxLag := rate / minF0

	if maxLag >= len(frame) {
		maxLag = len(frame) - 1
	}

	// d[tau] is the squared difference of the frame with itself shifted by
	// tau; d' is its cumulative-mean normalization (YIN steps 2–3).
	dPrime := make([]float64, maxLag+1)
	running := 0.0

	for tau := 1; tau <= maxLag; tau++ {
		d := 0.0

		for i := 0; i+tau < len(frame); i++ {
			diff := float64(frame[i]) - float64(frame[i+tau])
			d += diff * diff
		}

		running += d
		if running == 0 {
			dPrime[tau] = 1
		} else {
			dPrime[tau] = d * float64(tau) / running
		}
	}

	for tau := minLag; tau <= maxLag; tau++ {
		if dPrime[tau] < yinThreshold {
			// Refine to the local minimum of the dip.
			for tau+1 <= maxLag && dPrime[tau+1] < dPrime[tau] {
				tau++
			}

			return float64(rate) / float64(tau)
		}
	}

	return 0
}

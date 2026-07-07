package pipeline

import (
	"math"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// VADConfig tunes utterance endpointing.
type VADConfig struct {
	// Threshold is the RMS level, as a fraction of full scale, separating
	// speech from silence.
	Threshold float64
	// Silence endpoints an utterance after this much continuous quiet.
	Silence time.Duration
	// MaxUtterance hard-cuts an utterance regardless of activity.
	MaxUtterance time.Duration
	// MinUtterance drops activations shorter than this (clicks, coughs).
	MinUtterance time.Duration
}

// BroadcastVAD returns the endpointing defaults for the broadcast
// profile: 700 ms silence, 8 s hard cut.
func BroadcastVAD() VADConfig {
	return VADConfig{
		Threshold:    0.015,
		Silence:      700 * time.Millisecond,
		MaxUtterance: 8 * time.Second,
		MinUtterance: 250 * time.Millisecond,
	}
}

// CallVAD returns the endpointing defaults for the call profile:
// tighter 600 ms silence, 4 s hard cut for lower latency.
func CallVAD() VADConfig {
	return VADConfig{
		Threshold:    0.015,
		Silence:      600 * time.Millisecond,
		MaxUtterance: 4 * time.Second,
		MinUtterance: 250 * time.Millisecond,
	}
}

// EnergyVAD is the dependency-free energy endpointer implementing core.VAD;
// Feed allocates only when it emits an utterance.
type EnergyVAD struct {
	buf      []int16 // preallocated utterance storage
	cfg      VADConfig
	n        int           // samples buffered for the active utterance
	silence  int           // consecutive silent frames while active
	startPTS time.Duration // source PTS of buf[0]
	active   bool
}

// NewEnergyVAD preallocates for the configured maximum utterance.
func NewEnergyVAD(cfg VADConfig) *EnergyVAD {
	maxSamples := int(cfg.MaxUtterance/FrameDuration)*FrameSamples + FrameSamples

	return &EnergyVAD{cfg: cfg, buf: make([]int16, maxSamples)}
}

// Feed implements core.VAD.
func (v *EnergyVAD) Feed(frame core.PCM) []core.Utterance {
	speech := rms(frame.Data) >= v.cfg.Threshold

	if !v.active {
		if !speech {
			return nil
		}

		v.active = true
		v.startPTS = frame.PTS
	}

	v.append(frame.Data)

	if speech {
		v.silence = 0
	} else {
		v.silence++
	}

	if v.shouldEndpoint() {
		return v.endpoint()
	}

	return nil
}

// Flush implements core.VAD: it endpoints buffered speech when the source
// ends. A 7-second monologue followed by EOF is a caption, not garbage.
func (v *EnergyVAD) Flush() []core.Utterance {
	if !v.active {
		return nil
	}

	return v.endpoint()
}

// append buffers frame samples, clamped to the preallocated maximum; the
// hard cut fires before the clamp can ever drop audio.
func (v *EnergyVAD) append(samples []int16) {
	take := min(len(samples), len(v.buf)-v.n)
	copy(v.buf[v.n:], samples[:take])
	v.n += take
}

// shouldEndpoint reports whether the active utterance is complete: enough
// trailing silence, or the profile's hard cut.
func (v *EnergyVAD) shouldEndpoint() bool {
	if time.Duration(v.silence)*FrameDuration >= v.cfg.Silence {
		return true
	}

	return samplesDuration(v.n) >= v.cfg.MaxUtterance
}

// endpoint emits the buffered utterance and resets. Activations shorter than
// MinUtterance (net of trailing silence) are dropped as non-speech blips.
func (v *EnergyVAD) endpoint() []core.Utterance {
	speech := v.n - v.silence*FrameSamples
	utterance := core.Utterance{
		Audio: core.PCM{Rate: SampleRate, Ch: 1, PTS: v.startPTS},
		Final: true,
	}

	emit := samplesDuration(speech) >= v.cfg.MinUtterance
	if emit {
		// The internal buffer is reused immediately: the emitted audio must
		// be an owned copy.
		utterance.Audio.Data = append([]int16(nil), v.buf[:v.n]...)
	}

	v.active = false
	v.n = 0
	v.silence = 0

	if !emit {
		return nil
	}

	return []core.Utterance{utterance}
}

// rms computes root-mean-square energy as a fraction of full scale.
func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}

	var sum float64

	for _, s := range samples {
		f := float64(s) / math.MaxInt16
		sum += f * f
	}

	return math.Sqrt(sum / float64(len(samples)))
}

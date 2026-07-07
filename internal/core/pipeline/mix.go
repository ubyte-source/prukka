package pipeline

import (
	"math"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Sidechain envelope: the bed drops to its configured level
// while the dubbed voice speaks, with a 50 ms attack and 300 ms release.
const (
	duckAttack  = 50 * time.Millisecond
	duckRelease = 300 * time.Millisecond
	// duckThreshold separates speaking from silence on the voice track, as
	// a fraction of full scale.
	duckThreshold = 0.01
)

// Mixer renders one language's output: the bed ducked under the voice by
// a sidechain envelope; safe for concurrent use.
type Mixer struct {
	bed    *Track
	voice  *Track
	bedBuf []int16
	voxBuf []int16

	mu      sync.Mutex
	clock   time.Duration
	gain    float64 // current bed gain, smoothed
	bedGain float64 // ducked bed level, linear
	attack  float64 // per-sample smoothing toward the duck
	release float64 // per-sample smoothing back to full
	started bool
}

// NewMixer wires a mixer; bedDB is the ducked bed level in dB (default −15).
func NewMixer(bed, voice *Track, bedDB float64) *Mixer {
	return &Mixer{
		bed:     bed,
		voice:   voice,
		gain:    1,
		bedGain: math.Pow(10, bedDB/20),
		attack:  smoothing(duckAttack),
		release: smoothing(duckRelease),
	}
}

// smoothing computes the one-pole coefficient for a time constant.
func smoothing(tau time.Duration) float64 {
	return 1 - math.Exp(-1/(float64(SampleRate)*tau.Seconds()))
}

// Pull mixes the next n samples; false until the bed anchors, zero-fill
// past the live edge.
func (m *Mixer) Pull(n int) (core.PCM, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		start, ok := m.bed.Start()
		if !ok {
			return core.PCM{}, false
		}

		m.clock = start
		m.started = true
	}

	if len(m.bedBuf) < n {
		m.bedBuf = make([]int16, n)
		m.voxBuf = make([]int16, n)
	}

	bed := m.bedBuf[:n]
	vox := m.voxBuf[:n]

	m.bed.Window(m.clock, bed)
	m.voice.Window(m.clock, vox)

	out := core.PCM{Data: make([]int16, n), Rate: SampleRate, Ch: 1, PTS: m.clock}

	for i := range n {
		out.Data[i] = m.mixSample(bed[i], vox[i])
	}

	m.clock += durationFor(n)

	return out, true
}

// mixSample blends one sample pair, advancing the sidechain envelope.
func (m *Mixer) mixSample(bed, vox int16) int16 {
	speaking := math.Abs(float64(vox))/math.MaxInt16 > duckThreshold

	target := 1.0
	coeff := m.release

	if speaking {
		target = m.bedGain
		coeff = m.attack
	}

	m.gain += (target - m.gain) * coeff

	mixed := float64(bed)*m.gain + float64(vox)

	switch {
	case mixed > math.MaxInt16:
		return math.MaxInt16
	case mixed < math.MinInt16:
		return math.MinInt16
	default:
		return int16(mixed)
	}
}

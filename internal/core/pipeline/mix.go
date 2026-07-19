package pipeline

import (
	"context"
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
	bed   *Track
	voice *Track
	group *playoutGroup

	bedBuf []int16
	voxBuf []int16

	mu         sync.Mutex
	clock      time.Duration
	gain       float64 // current bed gain, smoothed
	bedGain    float64 // ducked bed level, linear
	attack     float64 // per-sample smoothing toward the duck
	release    float64 // per-sample smoothing back to full
	started    bool
	registered bool
	released   bool
	accepted   bool
	muted      bool // bed excluded from the mix (bed=off, calls)
}

// PullStatus distinguishes a temporarily unavailable live edge from the end
// of a finite timeline.
type PullStatus uint8

const (
	// PullPending means no window is ready yet, but more audio may arrive.
	PullPending PullStatus = iota
	// PullReady means the returned PCM contains the next playout window.
	PullReady
	// PullEOF means both input tracks are finished and fully consumed.
	PullEOF
)

// NewMixer wires a mixer; bedDB is the ducked bed level in dB (default −15),
// −Inf mutes the bed entirely: the sidechain otherwise releases it back to
// full volume whenever the voice pauses.
func NewMixer(bed, voice *Track, bedDB float64) *Mixer {
	muted := math.IsInf(bedDB, -1)
	bedGain := 0.0
	if !muted {
		bedGain = math.Pow(10, bedDB/20)
	}

	return &Mixer{
		bed:     bed,
		voice:   voice,
		gain:    1,
		bedGain: bedGain,
		attack:  smoothing(duckAttack),
		release: smoothing(duckRelease),
		group:   newPlayoutGroup(),
		muted:   muted,
	}
}

// Cursor returns an independent renderer over the same tracks (a Playout).
// Each output owns its clock, buffers and sidechain envelope, so concurrent
// consumers receive the complete timeline instead of advancing one shared
// cursor.
func (m *Mixer) Cursor() Playout {
	return m.cursor()
}

func (m *Mixer) cursor() *Mixer {
	return &Mixer{
		bed:     m.bed,
		voice:   m.voice,
		gain:    1,
		bedGain: m.bedGain,
		attack:  m.attack,
		release: m.release,
		group:   m.group,
		muted:   m.muted,
	}
}

// BeginPlayout registers this cursor as an active consumer. It is idempotent;
// false means finite playout was already sealed and the cursor must not start.
func (m *Mixer) BeginPlayout() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.beginPlayoutLocked()
}

func (m *Mixer) beginPlayoutLocked() bool {
	if !m.registered {
		m.registered = true
		m.accepted = m.group.acquire()
	}

	return m.accepted && !m.released
}

// ReleasePlayout acknowledges that this cursor's sink has stopped consuming.
// Consumers call it only after their final write and sink close have returned.
func (m *Mixer) ReleasePlayout() {
	m.mu.Lock()
	if !m.registered || !m.accepted || m.released {
		m.mu.Unlock()

		return
	}
	m.released = true
	m.mu.Unlock()

	m.group.release()
}

// WaitPlayout seals the consumer set and blocks until every started cursor
// acknowledges sink teardown. Cancellation bounds the wait.
func (m *Mixer) WaitPlayout(ctx context.Context) error {
	return m.group.wait(ctx)
}

// smoothing computes the one-pole coefficient for a time constant.
func smoothing(tau time.Duration) float64 {
	return 1 - math.Exp(-1/(float64(SampleRate)*tau.Seconds()))
}

// NextInto mixes into dst and reports whether the cursor is pending, ready or
// at EOF. The returned PCM aliases dst and the steady-state path allocates no
// memory.
func (m *Mixer) NextInto(dst []int16) (core.PCM, PullStatus) {
	return m.pull(len(dst), dst)
}

func (m *Mixer) pull(n int, outData []int16) (core.PCM, PullStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.beginPlayoutLocked() {
		return core.PCM{}, PullEOF
	}

	start, ok := m.playoutStart()
	if !ok {
		if _, finished := m.finiteEnd(); finished {
			return core.PCM{}, PullEOF
		}

		return core.PCM{}, PullPending
	}
	m.alignPlayoutClock(start)

	voiceEnd := m.voice.end()
	if end, finished := m.finiteEnd(); finished && m.clock >= end {
		return core.PCM{}, PullEOF
	}
	if !m.bed.ready(m.clock, n, voiceEnd) {
		return core.PCM{}, PullPending
	}

	out := m.render(n, outData)
	m.clock += durationFor(n)

	return out, PullReady
}

// alignPlayoutClock seeds the playout clock at the retained base on the first
// pull; thereafter the clock advances one quantum per pull and never chases
// the write head.
func (m *Mixer) alignPlayoutClock(start time.Duration) {
	if !m.started {
		m.clock = start
		m.started = true
	}
}

func (m *Mixer) playoutStart() (time.Duration, bool) {
	return m.bed.Start()
}

func (m *Mixer) render(n int, outData []int16) core.PCM {
	if len(m.bedBuf) < n {
		m.bedBuf = make([]int16, n)
		m.voxBuf = make([]int16, n)
	}

	bed := m.bedBuf[:n]
	vox := m.voxBuf[:n]
	m.voice.reserve(m.clock + durationFor(n))
	m.bed.Window(m.clock, bed)
	m.voice.Window(m.clock, vox)

	if outData == nil {
		outData = make([]int16, n)
	}
	out := core.PCM{Data: outData, Rate: SampleRate, Ch: 1, PTS: m.clock}
	for i := range n {
		out.Data[i] = m.mixSample(bed[i], vox[i])
	}

	return out
}

func (m *Mixer) finiteEnd() (time.Duration, bool) {
	if !m.bed.finished.Load() || !m.voice.finished.Load() {
		return 0, false
	}

	return max(m.bed.end(), m.voice.end()), true
}

// mixSample blends one sample pair, advancing the sidechain envelope.
func (m *Mixer) mixSample(bed, vox int16) int16 {
	if m.muted {
		return vox
	}

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

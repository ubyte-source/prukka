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
	return 1 - math.Exp(-1/(float64(core.SampleRate)*tau.Seconds()))
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

	// The bed consumes the voice tail only once it has finished (Track.ready);
	// in steady state the voice-track lock — the package's most contended —
	// is not worth acquiring for a value nobody reads.
	var voiceEnd time.Duration
	if m.bed.finished.Load() {
		voiceEnd = m.voice.end()
	}
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
	out := core.PCM{Data: outData, Rate: core.SampleRate, Ch: 1, PTS: m.clock}
	m.mixInto(out.Data[:n], bed, vox)

	return out
}

// duckThresholdS16 separates speaking from silence on the voice track: the
// smallest int16 magnitude above 1% of full scale (|vox| >= 328 iff
// |vox|/32767 > 0.01, the exact integer form of the former float comparison).
const duckThresholdS16 = 328

func (m *Mixer) mixInto(dst, bed, vox []int16) {
	if m.muted {
		copy(dst, vox)
		return
	}
	bed = bed[:len(vox)]
	dst = dst[:len(vox)]
	gain, bedGain := m.gain, m.bedGain
	attack, release := m.attack, m.release
	for i, v := range vox {
		target, coeff := 1.0, release
		if v >= duckThresholdS16 || v <= -duckThresholdS16 {
			target, coeff = bedGain, attack
		}
		gain += (target - gain) * coeff
		mixed := float64(bed[i])*gain + float64(v)
		switch {
		case mixed > math.MaxInt16:
			dst[i] = math.MaxInt16
		case mixed < math.MinInt16:
			dst[i] = math.MinInt16
		default:
			dst[i] = int16(mixed)
		}
	}
	m.gain = gain
}

func (m *Mixer) finiteEnd() (time.Duration, bool) {
	if !m.bed.finished.Load() || !m.voice.finished.Load() {
		return 0, false
	}

	return max(m.bed.end(), m.voice.end()), true
}

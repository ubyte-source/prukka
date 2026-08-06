package pipeline

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Sidechain envelope times for ducking the bed under the dubbed voice.
const (
	duckAttack  = 50 * time.Millisecond
	duckRelease = 300 * time.Millisecond
)

// Mixer is the broadcast playout: one language's output rendered as the bed
// ducked under the dubbed voice, with an independent render head per cursor
// over the one shared timeline. Safe for concurrent use.
type Mixer struct {
	bed   *Track
	voice *Track
	group *playoutGroup

	bedGain float64 // ducked bed level, linear
	attack  float64 // per-sample smoothing toward the duck
	release float64 // per-sample smoothing back to full
	muted   bool    // bed excluded from the mix (bed=off, calls)
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

// NewMixer wires a mixer; bedDB is the ducked bed level in dB (default −15)
// and −Inf mutes the bed entirely.
func NewMixer(bed, voice *Track, bedDB float64) *Mixer {
	muted := math.IsInf(bedDB, -1)
	bedGain := 0.0
	if !muted {
		bedGain = math.Pow(10, bedDB/20)
	}

	return &Mixer{
		bed:     bed,
		voice:   voice,
		bedGain: bedGain,
		attack:  smoothing(duckAttack),
		release: smoothing(duckRelease),
		group:   newPlayoutGroup(),
		muted:   muted,
	}
}

// Cursor returns a fresh, independent renderer over the same tracks.
func (m *Mixer) Cursor() Playout { return &mixCursor{mixer: m, gain: 1} }

// WaitPlayout seals the consumer set and blocks until every started cursor
// releases; cancellation bounds the wait.
func (m *Mixer) WaitPlayout(ctx context.Context) error {
	return m.group.wait(ctx)
}

// mixCursor is one sink's render head: its own playout clock, scratch buffers
// and sidechain envelope, under its own mutex.
type mixCursor struct {
	mixer  *Mixer
	bedBuf []int16
	voxBuf []int16

	mu         sync.Mutex
	clock      time.Duration
	gain       float64 // current bed gain, smoothed
	started    bool
	registered bool
	accepted   bool
	released   bool
}

// BeginPlayout registers this cursor as an active consumer; it is idempotent,
// and false means playout was already sealed.
func (c *mixCursor) BeginPlayout() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.beginLocked()
}

func (c *mixCursor) beginLocked() bool {
	if !c.registered {
		c.registered = true
		c.accepted = c.mixer.group.acquire()
		if c.accepted {
			// Only the voice track fences trim: the bed is shared by every
			// language's mixer, so one cursor must not pin it.
			c.mixer.voice.acquirePlayout()
		}
	}

	return c.accepted && !c.released
}

// ReleasePlayout retires this cursor and its trim fence.
func (c *mixCursor) ReleasePlayout() {
	c.mu.Lock()
	if !c.registered || !c.accepted || c.released {
		c.mu.Unlock()

		return
	}
	c.released = true
	c.mu.Unlock()

	c.mixer.voice.releasePlayout()
	c.mixer.group.release()
}

// smoothing computes the one-pole coefficient for a time constant.
func smoothing(tau time.Duration) float64 {
	return 1 - math.Exp(-1/(float64(core.SampleRate)*tau.Seconds()))
}

// NextInto mixes the next window into dst; the returned PCM aliases dst.
func (c *mixCursor) NextInto(dst []int16) (core.PCM, PullStatus) {
	n := len(dst)
	m := c.mixer

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginLocked() {
		return core.PCM{}, PullEOF
	}

	start, ok := m.playoutStart()
	if !ok {
		if _, finished := m.finiteEnd(); finished {
			return core.PCM{}, PullEOF
		}

		return core.PCM{}, PullPending
	}
	c.alignPlayoutClock(start, n)

	// Track.ready reads the voice tail only once the bed has finished; in
	// steady state, skip the package's most contended lock for an unread value.
	var voiceEnd time.Duration
	if m.bed.finished.Load() {
		voiceEnd = m.voice.end()
	}
	if end, finished := m.finiteEnd(); finished && c.clock >= end {
		return core.PCM{}, PullEOF
	}
	if !m.bed.ready(c.clock, n, voiceEnd) {
		return core.PCM{}, PullPending
	}

	out := c.render(dst)
	c.clock += durationFor(n)

	return out, PullReady
}

// alignPlayoutClock seeds the playout clock one quantum behind the newest
// instant the ready gate has released, so a cursor attached mid-session plays
// at the session's own delay instead of tens of seconds stale at the retention
// floor. Afterwards only trim moving the base past a stalled cursor may move
// it forward: dropped audio cannot be rendered and a silent window must not
// pass as ready.
func (c *mixCursor) alignPlayoutClock(start time.Duration, n int) {
	if c.started {
		c.clock = max(c.clock, start)

		return
	}

	c.clock = start
	if edge, ok := c.mixer.bed.dueEdge(); ok {
		c.clock = max(start, edge-durationFor(n))
	}
	c.started = true
}

func (m *Mixer) playoutStart() (time.Duration, bool) {
	return m.bed.Start()
}

func (c *mixCursor) render(dst []int16) core.PCM {
	n := len(dst)
	if len(c.bedBuf) < n {
		c.bedBuf = make([]int16, n)
		c.voxBuf = make([]int16, n)
	}

	bed := c.bedBuf[:n]
	vox := c.voxBuf[:n]
	c.mixer.voice.reserve(c.clock + durationFor(n))
	c.mixer.bed.Window(c.clock, bed)
	c.mixer.voice.Window(c.clock, vox)

	out := core.PCM{Data: dst, Rate: core.SampleRate, Ch: 1, PTS: c.clock}
	c.mixInto(out.Data, bed, vox)

	return out
}

// duckThresholdS16 separates speaking from silence on the voice track: the
// smallest int16 magnitude above 1% of full scale (|vox| >= 328 iff
// |vox|/32767 > 0.01).
const duckThresholdS16 = 328

func (c *mixCursor) mixInto(dst, bed, vox []int16) {
	m := c.mixer
	if m.muted {
		copy(dst, vox)

		return
	}
	bed = bed[:len(vox)]
	dst = dst[:len(vox)]
	gain, bedGain := c.gain, m.bedGain
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
	c.gain = gain
}

func (m *Mixer) finiteEnd() (time.Duration, bool) {
	if !m.bed.finished.Load() || !m.voice.finished.Load() {
		return 0, false
	}

	return max(m.bed.end(), m.voice.end()), true
}

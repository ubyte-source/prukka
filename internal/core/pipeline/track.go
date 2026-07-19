package pipeline

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// The live retention window plus trim hysteresis: memory stays bounded and
// Append stays amortized O(1).
const (
	trackKeep  = 30 * time.Second
	trackSlack = 15 * time.Second
)

// playoutCushion is the distance a delayed consumer keeps from the write head
// when the session delay is smaller, so real-time playout does not race the
// writer chunk by chunk.
const playoutCushion = 300 * time.Millisecond

// Track assembles one language's dubbed audio on the source clock:
// segments spill right rather than overwrite, gaps are silence, only the
// retention window stays in memory. Safe for concurrent use.
type Track struct {
	anchoredAt time.Time
	clock      playoutClock
	buf        []int16
	// mu is deliberate: trim and ready do cross-field arithmetic over
	// buf/base/first/floor/delay/anchoredAt that atomics cannot express, and
	// the six short acquisitions per mixer pull cost ~150ns of a 100ms
	// quantum. The right tool here is the lock.
	mu         sync.Mutex
	base       time.Duration // source PTS of buf[0]
	first      time.Duration
	floor      time.Duration
	delay      time.Duration
	configured bool
	finished   atomic.Bool
}

// NewTrack starts an empty track; the first append anchors its clock.
func NewTrack() *Track {
	return &Track{clock: systemClock{}}
}

// Append places samples at the given instant (already delayed), returning
// the instant actually used after spill.
func (t *Track) Append(at time.Duration, samples []int16) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	at = max(at, t.floor)
	if t.buf == nil {
		t.base = at
		t.first = at
		t.anchoredAt = t.clock.now()
	}

	offset := max(samplesFor(at-t.base),
		// Never overwrite placed speech: spill into what follows.
		len(t.buf))

	if grow := offset + len(samples); grow > len(t.buf) {
		t.buf = append(t.buf, make([]int16, grow-len(t.buf))...)
	}

	copy(t.buf[offset:], samples)
	placedAt := t.base + durationFor(offset)
	t.trim()

	return placedAt
}

// reserve prevents a producer from placing late audio in a rendered window.
func (t *Track) reserve(until time.Duration) {
	t.mu.Lock()
	t.floor = max(t.floor, until)
	t.mu.Unlock()
}

// ConfigurePlayout maps the delayed media clock onto real time; the engine
// calls it once per track before pumping.
func (t *Track) ConfigurePlayout(delay time.Duration) {
	t.mu.Lock()
	t.delay = max(delay, 0)
	t.configured = true
	t.mu.Unlock()
}

// finish closes the live edge so its final partial window can drain.
func (t *Track) finish() {
	t.finished.Store(true)
}

// Finish marks the track complete once the source ends: readiness then covers
// the buffered tail instead of stalling on the playout cushion that waits for
// more source audio, letting a finite lane drain its delayed dub.
func (t *Track) Finish() {
	t.finish()
}

// ready reports whether a complete live window, or the final partial one,
// is both acquired and due on the playout clock.
func (t *Track) ready(from time.Duration, samples int, tailEnd time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.buf == nil {
		return false
	}
	if !t.configured {
		return true
	}

	due := t.anchoredAt.Add(t.delay + from - t.first)
	if t.clock.now().Before(due) {
		return false
	}

	end := t.base + durationFor(len(t.buf))
	if t.finished.Load() {
		end = max(end, tailEnd)
	}
	if from >= end {
		return false
	}
	// The delay doubles as the live-edge cushion; a zero-delay call still
	// needs a floor, or the consumer races the writer chunk by chunk and
	// real-time playout stutters.
	if !t.finished.Load() && from+durationFor(samples)+max(t.delay, playoutCushion) > end {
		return false
	}

	return true
}

func (t *Track) end() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.buf == nil {
		return 0
	}

	return t.base + durationFor(len(t.buf))
}

// trim drops audio older than the retention window once the hysteresis
// slack fills, sliding the clock origin forward. Runs with t.mu held.
func (t *Track) trim() {
	keep := samplesFor(trackKeep + t.delay)
	if len(t.buf) <= keep+samplesFor(trackSlack) {
		return
	}
	// Never trim across the live playout fence: cutting unplayed audio would
	// slide the base past the consumer's clock and mute the track for good.
	// A zero floor means no live consumer, where retention rules alone.
	if t.floor > 0 && t.floor >= t.base {
		played := samplesFor(t.floor - t.base)
		keep = max(keep, len(t.buf)-played)
		if len(t.buf) <= keep+samplesFor(trackSlack) {
			return
		}
	}

	drop := len(t.buf) - keep
	t.base += durationFor(drop)
	kept := copy(t.buf, t.buf[drop:])
	t.buf = t.buf[:kept]
}

// Start reports the oldest retained instant (it slides forward); false
// until the first append.
func (t *Track) Start() (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.base, t.buf != nil
}

// Window copies the samples covering [from, from+len(dst)) into dst,
// zero-filling anything not placed.
func (t *Track) Window(from time.Duration, dst []int16) {
	t.mu.Lock()
	defer t.mu.Unlock()

	clear(dst)

	if t.buf == nil {
		return
	}

	offset := samplesFor(from - t.base)
	if from < t.base {
		// Requested window starts before the anchor: shift the copy right.
		lead := samplesFor(t.base - from)
		if lead >= len(dst) {
			return
		}

		copy(dst[lead:], t.buf[:min(len(t.buf), len(dst)-lead)])

		return
	}

	if offset >= len(t.buf) {
		return
	}

	copy(dst, t.buf[offset:])
}

// samplesFor converts a duration to reference-rate samples.
func samplesFor(d time.Duration) int {
	if d < 0 {
		return 0
	}

	return int(d * core.SampleRate / time.Second)
}

// durationFor converts reference-rate samples to a duration.
func durationFor(n int) time.Duration {
	return time.Duration(n) * time.Second / core.SampleRate
}

type playoutClock interface {
	now() time.Time
}

type systemClock struct{}

func (systemClock) now() time.Time {
	return time.Now()
}

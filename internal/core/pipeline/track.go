package pipeline

import (
	"sync"
	"time"
)

// The live retention window plus trim hysteresis: memory stays bounded and
// Append stays amortized O(1).
const (
	trackKeep  = 30 * time.Second
	trackSlack = 15 * time.Second
)

// Track assembles one language's dubbed audio on the source clock:
// segments spill right rather than overwrite, gaps are silence, only the
// retention window stays in memory. Safe for concurrent use.
type Track struct {
	buf  []int16
	mu   sync.Mutex
	base time.Duration // source PTS of buf[0]
}

// NewTrack starts an empty track; the first append anchors its clock.
func NewTrack() *Track {
	return &Track{}
}

// Append places samples at the given instant (already delayed), returning
// the instant actually used after spill.
func (t *Track) Append(at time.Duration, samples []int16) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.buf == nil {
		t.base = at
	}

	offset := samplesFor(at - t.base)
	if offset < len(t.buf) {
		// Never overwrite placed speech: spill into what follows.
		offset = len(t.buf)
	}

	if grow := offset + len(samples); grow > len(t.buf) {
		t.buf = append(t.buf, make([]int16, grow-len(t.buf))...)
	}

	copy(t.buf[offset:], samples)
	placedAt := t.base + durationFor(offset)

	t.trim()

	return placedAt
}

// trim drops audio older than the retention window once the hysteresis
// slack fills, sliding the clock origin forward. Runs with t.mu held.
func (t *Track) trim() {
	keep := samplesFor(trackKeep)
	if len(t.buf) <= keep+samplesFor(trackSlack) {
		return
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

	return int(d * SampleRate / time.Second)
}

// durationFor converts reference-rate samples to a duration.
func durationFor(n int) time.Duration {
	return time.Duration(n) * time.Second / SampleRate
}

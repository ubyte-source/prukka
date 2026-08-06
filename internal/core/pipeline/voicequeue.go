package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// VoiceQueue is the call-dub playout: a clockless FIFO of ready TTS takes that
// drain back-to-back at the reference rate, yields silence when empty, sheds
// its stalest audio past the lead cap, and gives every cursor an independent
// read head over the one shared timeline.
type VoiceQueue struct {
	cursors map[*voiceCursor]struct{}
	group   *playoutGroup
	buf     []int16

	// Append's shed compacts buf, clamps every cursor head and advances clock
	// as ONE invariant; the lock costs ~80ns per 20ms pull (0.0004% of the
	// tick). Do not "optimize" this into atomics.
	mu      sync.Mutex
	clock   time.Duration // monotonic PTS of buf[0]
	maxLead int           // backlog cap in samples; older excess dropped

	finished atomic.Bool
}

// NewVoiceQueue builds a call-dub queue whose unplayed backlog is capped at
// lead; a non-positive lead leaves it unbounded.
func NewVoiceQueue(lead time.Duration) *VoiceQueue {
	return &VoiceQueue{
		group:   newPlayoutGroup(),
		cursors: map[*voiceCursor]struct{}{},
		maxLead: samplesFor(max(lead, 0)),
	}
}

// Cursor returns a fresh, independent read head over the shared FIFO.
func (q *VoiceQueue) Cursor() Playout { return &voiceCursor{queue: q} }

// Append enqueues a complete take in arrival order — the instant is ignored —
// and returns the queue's end instant.
func (q *VoiceQueue) Append(_ time.Duration, samples []int16) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()

	if drop := q.consumedLocked(); drop > 0 {
		q.discardLocked(drop)
	}
	q.buf = append(q.buf, samples...)

	if q.maxLead > 0 && len(q.buf) > q.maxLead {
		q.discardLocked(len(q.buf) - q.maxLead)
	}

	return q.clock + durationFor(len(q.buf))
}

// consumedLocked is the prefix all live cursors have played.
func (q *VoiceQueue) consumedLocked() int {
	if len(q.cursors) == 0 {
		return 0
	}
	consumed := len(q.buf)
	for c := range q.cursors {
		consumed = min(consumed, c.head)
	}

	return consumed
}

// discardLocked drops the oldest n samples, advances the clock across them and
// clamps every cursor head.
func (q *VoiceQueue) discardLocked(n int) {
	q.buf = q.buf[:copy(q.buf, q.buf[n:])]
	q.clock += durationFor(n)
	for c := range q.cursors {
		c.head = max(0, c.head-n)
	}
}

// ConfigurePlayout is a no-op: a queue has no delayed media clock to map.
func (q *VoiceQueue) ConfigurePlayout(time.Duration) {}

// Finish seals the source so a drained queue reports EOF.
func (q *VoiceQueue) Finish() { q.finished.Store(true) }

// WaitPlayout seals the consumer set and blocks until every sink acknowledges
// teardown; cancellation bounds the wait.
func (q *VoiceQueue) WaitPlayout(ctx context.Context) error {
	return q.group.wait(ctx)
}

// voiceCursor is one sink's read head over the queue's shared timeline. All
// its fields are guarded by the queue's mu.
type voiceCursor struct {
	queue      *VoiceQueue
	head       int // samples of buf this sink has consumed
	registered bool
	accepted   bool
	released   bool
}

// NextInto copies this cursor's next window into dst, zero-padding a
// sub-quantum tail; the returned PCM aliases dst.
func (c *voiceCursor) NextInto(dst []int16) (core.PCM, PullStatus) {
	q := c.queue
	q.mu.Lock()
	defer q.mu.Unlock()

	if !c.beginLocked() {
		return core.PCM{}, PullEOF
	}

	pending := len(q.buf) - c.head
	if pending <= 0 {
		if q.finished.Load() {
			return core.PCM{}, PullEOF
		}

		return core.PCM{}, PullPending
	}

	pts := q.clock + durationFor(c.head)
	n := min(pending, len(dst))
	copy(dst[:n], q.buf[c.head:c.head+n])
	clear(dst[n:])
	c.head += n

	return core.PCM{Data: dst, Rate: core.SampleRate, Ch: 1, PTS: pts}, PullReady
}

// BeginPlayout registers this cursor as an active consumer; it is idempotent,
// and false means playout was already sealed.
func (c *voiceCursor) BeginPlayout() bool {
	q := c.queue
	q.mu.Lock()
	defer q.mu.Unlock()

	return c.beginLocked()
}

func (c *voiceCursor) beginLocked() bool {
	if !c.registered {
		c.registered = true
		c.accepted = c.queue.group.acquire()
		if c.accepted {
			c.queue.cursors[c] = struct{}{}
		}
	}

	return c.accepted && !c.released
}

// ReleasePlayout retires this cursor; a retired head no longer holds back
// compaction.
func (c *voiceCursor) ReleasePlayout() {
	q := c.queue
	q.mu.Lock()
	if !c.registered || !c.accepted || c.released {
		q.mu.Unlock()

		return
	}
	c.released = true
	delete(q.cursors, c)
	q.mu.Unlock()

	q.group.release()
}

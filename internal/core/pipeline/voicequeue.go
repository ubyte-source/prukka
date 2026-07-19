package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// VoiceQueue is the call-dub playout: a bare FIFO of ready TTS takes with no
// clock, no bed and no fixed delay. Takes enqueue as they are synthesized and
// drain back-to-back at the reference rate; an empty queue yields silence, and
// a backlog beyond maxLead sheds its stalest audio, because in a live call a
// stale translation is worse than a gap. The FIFO is one shared timeline;
// every cursor owns an independent read head over it, so the device push and
// a monitoring stream each hear the full feed.
type VoiceQueue struct {
	cursors map[*voiceCursor]struct{}
	group   *playoutGroup
	buf     []int16

	// mu is deliberate: Append's shed compacts buf, clamps every cursor head
	// and advances clock as ONE invariant — a lock-free ring forbids the
	// producer touching consumer cursors, and the measured cost of the lock
	// is ~80ns per 20ms pull (0.0004% of the tick). Do not "optimize" this
	// into atomics.
	mu      sync.Mutex
	clock   time.Duration // monotonic PTS of buf[0]
	maxLead int           // bounded unplayed backlog in samples; older excess dropped

	finished atomic.Bool
}

// NewVoiceQueue builds a call-dub queue whose unplayed backlog is capped at
// lead: past it the stalest audio is discarded, so mouth-to-ear latency stays
// bounded by the pipeline budget plus lead and self-heals to the budget on any
// pause. A non-positive lead leaves the backlog unbounded.
func NewVoiceQueue(lead time.Duration) *VoiceQueue {
	return &VoiceQueue{
		group:   newPlayoutGroup(),
		cursors: map[*voiceCursor]struct{}{},
		maxLead: samplesFor(max(lead, 0)),
	}
}

// Cursor returns a fresh, independent read head over the shared FIFO. Sharing
// one head would make concurrent sinks steal alternate windows from each
// other; a new cursor starts at the retained backlog (at most the lead cap)
// and catches up to live from there.
func (q *VoiceQueue) Cursor() Playout { return &voiceCursor{queue: q} }

// Append enqueues a complete take. A queue has no source timeline to place
// against — takes play in arrival order — so the instant is ignored; the
// returned value is the queue's end instant, for symmetry with the mixer's
// producer.
func (q *VoiceQueue) Append(_ time.Duration, samples []int16) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Reclaim the prefix every live cursor has consumed so capacity is
	// reused as a bounded ring; with no live cursor nothing is consumed and
	// only the lead cap below bounds the buffer.
	if drop := q.consumedLocked(); drop > 0 {
		q.discardLocked(drop)
	}
	q.buf = append(q.buf, samples...)

	// Shed the stalest over-cap audio, advancing the clock across the gap
	// and clamping heads, so the newest speech reaches every ear within the
	// lead bound.
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

// discardLocked drops the oldest n samples, keeping every cursor on the same
// audio it was on — or at the new start, when its unplayed span was shed.
func (q *VoiceQueue) discardLocked(n int) {
	q.buf = q.buf[:copy(q.buf, q.buf[n:])]
	q.clock += durationFor(n)
	for c := range q.cursors {
		c.head = max(0, c.head-n)
	}
}

// ConfigurePlayout is a no-op: a queue has no delayed media clock to map onto
// real time.
func (q *VoiceQueue) ConfigurePlayout(time.Duration) {}

// Finish seals the source so a drained queue reports EOF and a finite lane can
// terminate.
func (q *VoiceQueue) Finish() { q.finished.Store(true) }

// WaitPlayout seals the consumer set and blocks until every sink acknowledges
// teardown; cancellation bounds the wait.
func (q *VoiceQueue) WaitPlayout(ctx context.Context) error {
	return q.group.wait(ctx)
}

// voiceCursor is one sink's read head over the queue's shared timeline. All
// fields are guarded by the queue's mu: head takes part in the queue's
// compact/shed invariant, so a cursor-local lock could not protect it.
type voiceCursor struct {
	queue      *VoiceQueue
	head       int // samples of buf this sink has consumed
	registered bool
	accepted   bool
	released   bool
}

// NextInto copies this cursor's next window into dst: PullReady with the
// filled (and, on a sub-quantum tail, zero-padded) window, PullPending when
// the cursor has drained the queue but the source is unfinished, or PullEOF
// once finished and fully drained. The returned PCM aliases dst and the
// steady-state path allocates nothing.
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

// BeginPlayout registers this cursor as an active consumer. It is idempotent;
// false means finite playout was already sealed and the cursor must not start.
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

// ReleasePlayout acknowledges that this cursor's sink has stopped consuming.
// A retired head no longer holds back the queue's compaction.
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

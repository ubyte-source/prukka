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
// stale translation is worse than a gap. A call drives one device push, so the
// queue is its own single cursor and satisfies both Template and Playout.
type VoiceQueue struct {
	group *playoutGroup
	buf   []int16

	// mu is deliberate: Append's shed compacts buf, moves the consumer-owned
	// head and advances clock as ONE invariant — a lock-free SPSC ring forbids
	// the producer touching the consumer cursor, and the measured cost of the
	// lock is ~80ns per 20ms pull (0.0004% of the tick). Do not "optimize"
	// this into atomics.
	mu      sync.Mutex
	maxLead int           // bounded unplayed backlog in samples; older excess dropped
	head    int           // samples already handed to the sink
	clock   time.Duration // monotonic PTS of the next window

	finished atomic.Bool
	state    cursorState
}

// cursorState is the queue's consumer lifecycle. The queue is a single read
// head with SUCCESSION: a released consumer may be replaced by a fresh one
// (a re-pushed device route) until the group seals.
type cursorState uint8

const (
	cursorIdle cursorState = iota
	cursorLive
	cursorReleased
	cursorRefused
)

// NewVoiceQueue builds a call-dub queue whose unplayed backlog is capped at
// lead: past it the stalest audio is discarded, so mouth-to-ear latency stays
// bounded by the pipeline budget plus lead and self-heals to the budget on any
// pause. A non-positive lead leaves the backlog unbounded.
func NewVoiceQueue(lead time.Duration) *VoiceQueue {
	return &VoiceQueue{
		group:   newPlayoutGroup(),
		maxLead: samplesFor(max(lead, 0)),
	}
}

// Cursor returns the queue itself: a call drives one device push, so there is
// one consumer over one read head.
func (q *VoiceQueue) Cursor() Playout { return q }

// Append enqueues a complete take. A queue has no source timeline to place
// against — takes play in arrival order — so the instant is ignored; the
// returned value is the queue's end instant, for symmetry with the mixer's
// producer.
func (q *VoiceQueue) Append(_ time.Duration, samples []int16) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Reclaim the consumed prefix so capacity is reused as a bounded ring.
	if q.head > 0 {
		q.buf = q.buf[:copy(q.buf, q.buf[q.head:])]
		q.head = 0
	}
	q.buf = append(q.buf, samples...)

	// Shed the stalest over-cap audio, advancing the clock across the gap so
	// the newest speech always reaches the ear within the lead bound.
	if q.maxLead > 0 && len(q.buf) > q.maxLead {
		drop := len(q.buf) - q.maxLead
		q.buf = q.buf[:copy(q.buf, q.buf[drop:])]
		q.clock += durationFor(drop)
	}

	return q.clock + durationFor(len(q.buf))
}

// ConfigurePlayout is a no-op: a queue has no delayed media clock to map onto
// real time.
func (q *VoiceQueue) ConfigurePlayout(time.Duration) {}

// Finish seals the source so a drained queue reports EOF and a finite lane can
// terminate.
func (q *VoiceQueue) Finish() { q.finished.Store(true) }

// NextInto copies the next window into dst: PullReady with the filled (and, on
// a sub-quantum tail, zero-padded) window, PullPending when the queue is empty
// but unfinished, or PullEOF once finished and fully drained. The returned PCM
// aliases dst and the steady-state path allocates nothing.
func (q *VoiceQueue) NextInto(dst []int16) (core.PCM, PullStatus) {
	q.mu.Lock()
	defer q.mu.Unlock()

	pending := len(q.buf) - q.head
	if pending <= 0 {
		if q.finished.Load() {
			return core.PCM{}, PullEOF
		}

		return core.PCM{}, PullPending
	}

	pts := q.clock
	n := min(pending, len(dst))
	copy(dst[:n], q.buf[q.head:q.head+n])
	clear(dst[n:])
	q.head += n
	q.clock += durationFor(n)

	return core.PCM{Data: dst, Rate: core.SampleRate, Ch: 1, PTS: pts}, PullReady
}

// BeginPlayout registers this cursor as an active consumer. It is idempotent
// for the live consumer, and a released consumer may be SUCCEEDED by a new
// one: the queue is a single read head, not a single-use one — a replaced
// push route re-registers here, where a one-shot gate would refuse every
// re-push with "not ready" forever. False means finite playout was already
// sealed and the cursor must not start.
func (q *VoiceQueue) BeginPlayout() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	switch q.state {
	case cursorLive:
		return true // idempotent for the live consumer
	case cursorRefused:
		return false // sealed playout stays sealed
	default: // idle or released: admit (or succeed) a consumer
		if q.group.acquire() {
			q.state = cursorLive

			return true
		}
		q.state = cursorRefused

		return false
	}
}

// ReleasePlayout acknowledges that this cursor's sink has stopped consuming.
func (q *VoiceQueue) ReleasePlayout() {
	q.mu.Lock()
	if q.state != cursorLive {
		q.mu.Unlock()

		return
	}
	q.state = cursorReleased
	q.mu.Unlock()

	q.group.release()
}

// WaitPlayout seals the consumer set and blocks until the sink acknowledges
// teardown; cancellation bounds the wait.
func (q *VoiceQueue) WaitPlayout(ctx context.Context) error {
	return q.group.wait(ctx)
}

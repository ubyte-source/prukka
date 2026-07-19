package pipeline

import (
	"context"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// Playout is a real-time playout cursor: a sink drains it one quantum per
// tick with NextInto. Every implementation upholds three invariants, so a sink
// can treat any Playout identically:
//
//   - Monotonic clock: successive ready windows return a non-decreasing
//     PCM.PTS; the cursor never rewinds, even across a dropped span.
//   - Bounded latency without a fixed delay: audio is delivered close to real
//     time and its lag is bounded — the call queue drops its stalest excess,
//     the broadcast mixer rides its delayed retention window — never an
//     unbounded or growing delay.
//   - Silence on underrun: when no window is ready the cursor reports
//     PullPending; the sink fills one silent quantum and the next take plays on
//     the following tick, so a device queue never wedges.
//
// A window a sink is currently draining is never truncated: the call queue
// sheds audio only at its stale end on enqueue. A broadcast *Mixer (bed ducked
// under voice on a retained, delayed timeline) and a call *VoiceQueue (a bare
// FIFO of ready takes) both satisfy this contract.
type Playout interface {
	// NextInto mixes or copies the next window into dst and reports whether the
	// cursor is pending, ready or at EOF. The returned PCM aliases dst.
	NextInto(dst []int16) (core.PCM, PullStatus)
	// BeginPlayout registers this cursor as an active consumer; false means the
	// finite timeline was already sealed and the cursor must not start.
	BeginPlayout() bool
	// ReleasePlayout acknowledges that this cursor's sink has stopped consuming.
	ReleasePlayout()
}

// Template produces independent Playout cursors over one shared timeline and
// blocks until their sinks drain. It is the seam every encoder job reads
// through; both the broadcast Mixer and the call VoiceQueue implement it.
type Template interface {
	// Cursor returns a fresh, independent Playout over the same timeline.
	Cursor() Playout
	// WaitPlayout seals the consumer set and blocks until every started cursor
	// acknowledges sink teardown; cancellation bounds the wait.
	WaitPlayout(ctx context.Context) error
}

// playoutGroup accounts for consumers of one mixer template. A finite
// producer seals the group before waiting, so consumers cannot join behind
// its completion snapshot.
type playoutGroup struct {
	done   chan struct{}
	active int
	sealed bool
	mu     sync.Mutex
}

func newPlayoutGroup() *playoutGroup {
	return &playoutGroup{done: make(chan struct{})}
}

func (g *playoutGroup) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.sealed {
		return false
	}

	g.active++

	return true
}

func (g *playoutGroup) release() {
	g.mu.Lock()
	g.active--
	if g.sealed && g.active == 0 {
		close(g.done)
	}
	g.mu.Unlock()
}

func (g *playoutGroup) wait(ctx context.Context) error {
	g.mu.Lock()
	if !g.sealed {
		g.sealed = true
		if g.active == 0 {
			close(g.done)
		}
	}
	done := g.done
	g.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

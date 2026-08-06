package pipeline

import (
	"context"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// Playout is a real-time playout cursor a sink drains one quantum per tick.
// Every implementation upholds the same invariants: PCM.PTS never decreases
// across ready windows, lag stays bounded without a fixed delay, an underrun
// reports PullPending instead of blocking so a device queue never wedges, and
// a window a sink is draining is never truncated.
type Playout interface {
	// NextInto fills dst with the next window; the returned PCM aliases dst.
	NextInto(dst []int16) (core.PCM, PullStatus)
	// BeginPlayout registers this cursor as an active consumer; false means the
	// finite timeline was already sealed and the cursor must not start.
	BeginPlayout() bool
	// ReleasePlayout acknowledges that this cursor's sink has stopped consuming;
	// call it only after the final write and sink close have returned.
	ReleasePlayout()
}

// Template produces independent Playout cursors over one shared timeline and
// blocks until their sinks drain.
type Template interface {
	// Cursor returns a fresh, independent Playout over the same timeline.
	Cursor() Playout
	// WaitPlayout seals the consumer set and blocks until every started cursor
	// releases; cancellation bounds the wait.
	WaitPlayout(ctx context.Context) error
}

// playoutGroup accounts for one template's consumers; the seal a finite
// producer takes before waiting keeps consumers from joining behind it.
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

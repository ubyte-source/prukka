package pipeline

import (
	"context"
	"sync"
)

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

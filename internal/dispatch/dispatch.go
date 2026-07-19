// Package dispatch is the bounded worker pool between the pipeline and speech
// providers: a buffered channel carries jobs, blocking submitters only at the
// full edge and parking workers only at the empty one.
package dispatch

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Submit after Close has been called.
var ErrClosed = errors.New("dispatch: pool is closed")

// Pool runs jobs on a fixed worker set behind a bounded queue. Construct it
// with New and release it with Close. Sizing comes validated from
// configuration (providers.dispatch), the single source of truth for bounds.
type Pool struct {
	queue    chan func()
	done     chan struct{} // closed by Close to release parked workers
	workers  sync.WaitGroup
	inflight sync.WaitGroup
	mu       sync.Mutex // serializes the accept edge against Close
	closed   bool       // guarded by mu
}

// New starts a pool of `workers` goroutines with a queue that holds up to
// `queue` pending jobs. Both must be positive — configuration
// (providers.dispatch) validates the real bounds; this guard turns a wiring
// mistake into an immediate panic instead of a pool that hangs.
func New(workers, queue int) *Pool {
	if workers < 1 || queue < 1 {
		panic("dispatch: workers and queue must be positive")
	}

	p := &Pool{
		queue: make(chan func(), queue),
		done:  make(chan struct{}),
	}

	p.workers.Add(workers)
	for range workers {
		go p.worker()
	}

	return p
}

// Submit enqueues fn for a worker, blocking while the queue is full;
// ctx.Err() if ctx ends first, ErrClosed after Close.
func (p *Pool) Submit(ctx context.Context, fn func()) error {
	// The accept edge is a critical section: an inflight.Add racing Close
	// past its Wait would strand an accepted job.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()

		return ErrClosed
	}

	p.inflight.Add(1)
	p.mu.Unlock()

	select {
	case p.queue <- fn:
		return nil
	case <-ctx.Done():
		p.inflight.Done()

		return ctx.Err()
	}
}

// worker consumes jobs until Close releases it; receiving frees the queue
// slot before the slow body runs. Close waits for every accepted job first,
// so nothing is left to drain.
func (p *Pool) worker() {
	defer p.workers.Done()

	for {
		select {
		case fn := <-p.queue:
			p.run(fn)
		case <-p.done:
			return
		}
	}
}

func (p *Pool) run(fn func()) {
	defer p.inflight.Done()

	fn()
}

// Close stops accepting new work, waits for all submitted jobs to finish,
// then stops the workers. It is safe to call once; further calls are no-ops.
func (p *Pool) Close() {
	p.mu.Lock()
	stale := p.closed
	p.closed = true
	p.mu.Unlock()

	if stale {
		return
	}

	p.inflight.Wait() // every accepted job has run
	close(p.done)     // release idle workers
	p.workers.Wait()
}

// Metrics is a best-effort snapshot of queue saturation for observability.
type Metrics struct {
	Size     uint64
	Capacity uint64
}

// Metrics reports the pending-job count against the queue capacity.
func (p *Pool) Metrics() Metrics {
	return Metrics{Size: uint64(len(p.queue)), Capacity: uint64(cap(p.queue))}
}

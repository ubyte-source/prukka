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

// Pool runs jobs on a fixed worker set behind a bounded queue; build it with
// New and release it with Close.
type Pool struct {
	queue    chan func()
	done     chan struct{}
	workers  sync.WaitGroup
	inflight sync.WaitGroup
	mu       sync.Mutex // serializes the accept edge against Close
	closed   bool       // guarded by mu
}

// New starts a pool of `workers` goroutines with a queue that holds up to
// `queue` pending jobs; it panics unless both are positive.
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
	// An inflight.Add racing Close past its Wait would strand an accepted job.
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

// Close stops accepting new work, waits for every accepted job, then stops the
// workers; further calls are no-ops.
func (p *Pool) Close() {
	p.mu.Lock()
	stale := p.closed
	p.closed = true
	p.mu.Unlock()

	if stale {
		return
	}

	p.inflight.Wait()
	close(p.done)
	p.workers.Wait()
}

// Metrics is a best-effort snapshot of queue saturation.
type Metrics struct {
	Size     uint64
	Capacity uint64
}

// Metrics reports the pending-job count against the queue capacity.
func (p *Pool) Metrics() Metrics {
	return Metrics{Size: uint64(len(p.queue)), Capacity: uint64(cap(p.queue))}
}

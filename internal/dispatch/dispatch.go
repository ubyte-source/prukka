// Package dispatch is the bounded worker pool between the pipeline and speech
// providers: the lock-free ring carries jobs, two conserved token
// channels block only at the empty and full edges.
package dispatch

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/ubyte-source/prukka/internal/ring"
)

// ErrClosed is returned by Submit after Close has been called.
var ErrClosed = errors.New("dispatch: pool is closed")

// Defaults apply when New is given a non-positive worker count or queue
// depth.
const (
	defaultWorkers = 8
	defaultQueue   = 256
)

// task is one unit of work; the pool stores pointers to these in the ring.
type task struct {
	fn func()
}

// Pool runs jobs on a fixed worker set backed by an MPMC ring. Construct it
// with New and release it with Close.
type Pool struct {
	ring     *ring.Ring[task]
	items    chan struct{} // one token per queued job; parks idle workers
	space    chan struct{} // one token per free slot; blocks full submitters
	done     chan struct{} // closed by Close to release parked workers
	workers  sync.WaitGroup
	inflight sync.WaitGroup
	mu       sync.Mutex // serializes the accept edge against Close
	closed   bool       // guarded by mu
}

// New starts a pool of `workers` goroutines with a queue that holds up to
// `queue` pending jobs. Both fall back to defaults when non-positive.
func New(workers, queue int) *Pool {
	if workers <= 0 {
		workers = defaultWorkers
	}

	if queue <= 0 {
		queue = defaultQueue
	}

	p := &Pool{
		ring:  ring.New[task](uint64(queue), uint64(queue)),
		items: make(chan struct{}, queue),
		space: make(chan struct{}, queue),
		done:  make(chan struct{}),
	}

	// Prefill the space tokens: every slot is free at start.
	for range queue {
		p.space <- struct{}{}
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
	case <-p.space:
	case <-ctx.Done():
		p.inflight.Done()

		return ctx.Err()
	}

	// The space token guarantees room, but Enqueue can exhaust its CAS
	// budget under contention; dropping here would lose the job, so retry.
	t := &task{fn: fn}
	for !p.ring.Enqueue(t) {
		runtime.Gosched()
	}

	p.items <- struct{}{} // conserved: never blocks

	return nil
}

// worker consumes jobs until Close releases it; Close waits for every
// accepted job first, so nothing is left to drain.
func (p *Pool) worker() {
	defer p.workers.Done()

	for {
		select {
		case <-p.items:
			p.run()
		case <-p.done:
			return
		}
	}
}

// run executes one job, freeing its queue slot before the slow body; an
// empty dequeue is a transient publish window, so retry.
func (p *Pool) run() {
	defer p.inflight.Done()

	t, ok := p.ring.Dequeue()
	for !ok {
		runtime.Gosched()

		t, ok = p.ring.Dequeue()
	}

	p.space <- struct{}{} // conserved: returns the slot this job occupied

	t.fn()
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

// Metrics exposes the queue's best-effort counters for observability.
func (p *Pool) Metrics() ring.Metrics { return p.ring.GetMetrics() }

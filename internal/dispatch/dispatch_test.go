package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/dispatch"
)

// mustSubmit fails the test if a job the pool should accept is rejected.
func mustSubmit(t *testing.T, p *dispatch.Pool, fn func()) {
	t.Helper()

	if err := p.Submit(context.Background(), fn); err != nil {
		t.Fatalf("Submit returned %v, want acceptance", err)
	}
}

func TestSubmitRunsEveryJob(t *testing.T) {
	t.Parallel()

	p := dispatch.New(4, 16)
	defer p.Close()

	const jobs = 500

	var ran atomic.Int64

	var wg sync.WaitGroup

	wg.Add(jobs)

	for range jobs {
		if err := p.Submit(context.Background(), func() {
			defer wg.Done()

			ran.Add(1)
		}); err != nil {
			t.Fatalf("Submit returned %v", err)
		}
	}

	wg.Wait()

	if ran.Load() != jobs {
		t.Fatalf("ran %d jobs, want %d", ran.Load(), jobs)
	}
}

func TestConcurrencyNeverExceedsWorkers(t *testing.T) {
	t.Parallel()

	const workers = 3

	p := dispatch.New(workers, 32)
	defer p.Close()

	var live, peak atomic.Int64

	var wg sync.WaitGroup

	wg.Add(50)

	for range 50 {
		if err := p.Submit(context.Background(), func() {
			defer wg.Done()

			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}

			time.Sleep(time.Millisecond) // hold the worker so overlap is observable
			live.Add(-1)
		}); err != nil {
			t.Fatalf("Submit returned %v", err)
		}
	}

	wg.Wait()

	if peak.Load() > workers {
		t.Fatalf("observed %d concurrent jobs, want at most %d workers", peak.Load(), workers)
	}

	if peak.Load() == 0 {
		t.Fatal("no job ran")
	}
}

func TestSubmitBlocksWhenQueueFullThenDrains(t *testing.T) {
	t.Parallel()

	// One worker, queue depth one: the second in-flight job must wait for a
	// slot, so Submit provides backpressure rather than dropping work.
	p := dispatch.New(1, 1)
	defer p.Close()

	release := make(chan struct{})
	started := make(chan struct{})

	var done atomic.Int64

	// Job A occupies the single worker until released.
	if err := p.Submit(context.Background(), func() {
		close(started)
		<-release
		done.Add(1)
	}); err != nil {
		t.Fatalf("Submit A: %v", err)
	}

	<-started

	// Job B fills the one queue slot (accepted, waiting for the worker).
	if err := p.Submit(context.Background(), func() { done.Add(1) }); err != nil {
		t.Fatalf("Submit B: %v", err)
	}

	// Job C cannot be accepted until A frees the worker and B moves out of
	// the queue. Prove Submit blocks by racing it against a short deadline.
	blocked := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()

		blocked <- p.Submit(ctx, func() { done.Add(1) })
	}()

	if err := <-blocked; err == nil {
		t.Fatal("Submit C succeeded while the queue was full; expected backpressure to hold it")
	}

	// Release the pipeline: A and B complete.
	close(release)
	p.Close()

	if done.Load() < 2 {
		t.Fatalf("completed %d jobs, want at least A and B", done.Load())
	}
}

func TestSubmitHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	p := dispatch.New(1, 1)
	defer p.Close()

	block := make(chan struct{})
	defer close(block)

	// Saturate the worker and the single slot.
	mustSubmit(t, p, func() { <-block })
	mustSubmit(t, p, func() { <-block })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.Submit(ctx, func() {}); err == nil {
		t.Fatal("Submit returned nil for a canceled context on a full queue")
	}
}

func TestSubmitAfterCloseReturnsErrClosed(t *testing.T) {
	t.Parallel()

	p := dispatch.New(2, 4)
	p.Close()

	if err := p.Submit(context.Background(), func() {}); !errors.Is(err, dispatch.ErrClosed) {
		t.Fatalf("Submit after Close = %v, want ErrClosed", err)
	}
}

func TestCloseDrainsAcceptedJobs(t *testing.T) {
	t.Parallel()

	p := dispatch.New(2, 64)

	const jobs = 100

	var ran atomic.Int64

	for range jobs {
		if err := p.Submit(context.Background(), func() {
			time.Sleep(100 * time.Microsecond)
			ran.Add(1)
		}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	p.Close() // must not return until every accepted job has run

	if ran.Load() != jobs {
		t.Fatalf("Close drained %d jobs, want all %d", ran.Load(), jobs)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	p := dispatch.New(2, 4)
	p.Close()
	p.Close() // second call must be a no-op, not a panic
}

func TestMetricsReportSaturationAgainstCapacity(t *testing.T) {
	t.Parallel()

	p := dispatch.New(2, 16)

	const jobs = 40

	var wg sync.WaitGroup

	wg.Add(jobs)

	for range jobs {
		mustSubmit(t, p, wg.Done)
	}

	wg.Wait()
	p.Close()

	m := p.Metrics()
	if m.Size != 0 || m.Capacity != 16 {
		t.Fatalf("drained metrics = %d/%d, want 0 pending over capacity 16", m.Size, m.Capacity)
	}
}

// TestManyProducersExactlyOnce: every concurrently submitted job runs
// exactly once under the race detector.
func TestManyProducersExactlyOnce(t *testing.T) {
	t.Parallel()

	p := dispatch.New(8, 128)

	const (
		producers   = 8
		perProducer = 2000
		total       = producers * perProducer
	)

	var ran atomic.Int64

	var seen sync.Map

	var dupes, submitErrs atomic.Int64

	var wg sync.WaitGroup

	wg.Add(total)

	var pg sync.WaitGroup

	pg.Add(producers)

	for pr := range producers {
		go func() {
			defer pg.Done()

			for i := range perProducer {
				id := pr*perProducer + i
				if err := p.Submit(context.Background(), func() {
					defer wg.Done()

					if _, dup := seen.LoadOrStore(id, struct{}{}); dup {
						dupes.Add(1)
					}

					ran.Add(1)
				}); err != nil {
					submitErrs.Add(1)

					wg.Done() // the job will not run; keep the wait balanced
				}
			}
		}()
	}

	pg.Wait()
	wg.Wait()
	p.Close()

	if submitErrs.Load() != 0 {
		t.Fatalf("%d submits were rejected on a background context", submitErrs.Load())
	}

	if ran.Load() != total {
		t.Fatalf("ran %d jobs, want %d", ran.Load(), total)
	}

	if dupes.Load() != 0 {
		t.Fatalf("%d jobs ran more than once", dupes.Load())
	}
}

// TestSubmitCloseRaceNeverStrandsJobs: a submit racing Close is either
// rejected or fully executed before Close returns.
func TestSubmitCloseRaceNeverStrandsJobs(t *testing.T) {
	t.Parallel()

	const rounds, submitters = 300, 8

	for range rounds {
		p := dispatch.New(2, 4)

		var accepted, ran atomic.Int64

		var wg sync.WaitGroup

		wg.Add(submitters)
		for range submitters {
			go func() {
				defer wg.Done()

				if p.Submit(context.Background(), func() { ran.Add(1) }) == nil {
					accepted.Add(1)
				}
			}()
		}

		p.Close()
		wg.Wait()

		if accepted.Load() != ran.Load() {
			t.Fatalf("accepted %d jobs but ran %d: a submit racing Close was stranded", accepted.Load(), ran.Load())
		}
	}
}

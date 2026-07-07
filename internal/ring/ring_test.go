package ring_test

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ubyte-source/prukka/internal/ring"
)

// item is a small payload for the pointer ring.
type item struct{ n int }

func TestNewRoundsCapacityToPowerOfTwo(t *testing.T) {
	t.Parallel()

	r := ring.New[item](100, 100)
	if capacity := r.Capacity(); capacity&(capacity-1) != 0 || capacity < 100 {
		t.Fatalf("capacity = %d, want a power of two ≥ 100", capacity)
	}
}

func TestNewDefaultsAndLogicalCapacity(t *testing.T) {
	t.Parallel()

	def := ring.New[item](0, 0)
	if def.Capacity() == 0 || def.GetMetrics().LogicalCapacity == 0 {
		t.Fatal("default ring has zero capacity")
	}

	r := ring.New[item](8, 1_000_000)
	if m := r.GetMetrics(); m.LogicalCapacity > m.Capacity {
		t.Fatalf("oversized logical cap %d not clamped to physical %d", m.LogicalCapacity, m.Capacity)
	}
}

func TestEnqueueDequeueFIFO(t *testing.T) {
	t.Parallel()

	r := ring.New[item](8, 8)

	for i := range 5 {
		if !r.Enqueue(&item{n: i}) {
			t.Fatalf("Enqueue(%d) returned false", i)
		}
	}

	if r.Size() != 5 {
		t.Fatalf("Size = %d, want 5", r.Size())
	}

	for i := range 5 {
		got, ok := r.Dequeue()
		if !ok || got.n != i {
			t.Fatalf("Dequeue = %v/%v, want item %d", got, ok, i)
		}
	}

	if _, ok := r.Dequeue(); ok {
		t.Fatal("Dequeue on an empty ring returned ok")
	}
}

func TestEnqueueRejectsWhenFull(t *testing.T) {
	t.Parallel()

	r := ring.New[item](4, 4)

	filled := 0
	for r.Enqueue(&item{n: filled}) {
		filled++
	}

	if filled == 0 || !r.IsFull() {
		t.Fatalf("filled=%d full=%v, want a full ring", filled, r.IsFull())
	}

	if r.GetMetrics().DroppedByCapacity == 0 {
		t.Fatal("capacity drop not counted")
	}
}

func TestWrapAroundKeepsIntegrity(t *testing.T) {
	t.Parallel()

	r := ring.New[item](4, 4)

	for lap := range 20 {
		if !r.Enqueue(&item{n: lap}) {
			t.Fatalf("Enqueue lap %d failed", lap)
		}

		got, ok := r.Dequeue()
		if !ok || got.n != lap {
			t.Fatalf("lap %d dequeued %v/%v", lap, got, ok)
		}
	}
}

func TestMetricsCountEnqueuesAndDequeues(t *testing.T) {
	t.Parallel()

	r := ring.New[item](8, 8)
	for i := range 3 {
		r.Enqueue(&item{n: i})
	}

	r.Dequeue()

	m := r.GetMetrics()
	if m.EnqueueCount != 3 || m.DequeueCount != 1 || m.Size != 2 {
		t.Fatalf("metrics = %+v, want 3 enq / 1 deq / size 2", m)
	}
}

func TestMPMCDeliversEveryItemExactlyOnce(t *testing.T) {
	t.Parallel()

	const (
		producers   = 4
		consumers   = 4
		perProducer = 5000
		total       = producers * perProducer
	)

	r := ring.New[item](1<<12, 1<<12)
	tally := &mpmcTally{}

	// Producers and consumers must run concurrently: the ring holds far
	// fewer items than the total.
	var pwg, cwg sync.WaitGroup

	startProducers(r, &pwg, producers, perProducer, tally)
	startConsumers(r, &cwg, consumers, tally)

	pwg.Wait()
	tally.producersDone.Store(true)
	cwg.Wait()

	if tally.produced.Load() != total || tally.consumed.Load() != total {
		t.Fatalf("produced %d consumed %d, want %d each",
			tally.produced.Load(), tally.consumed.Load(), total)
	}

	if tally.dupes.Load() != 0 {
		t.Fatalf("%d duplicate deliveries — exactly-once violated", tally.dupes.Load())
	}
}

// mpmcTally aggregates the concurrent producer/consumer counters.
type mpmcTally struct {
	seen          sync.Map // n -> struct{}, detects duplicate deliveries
	produced      atomic.Int64
	consumed      atomic.Int64
	dupes         atomic.Int64
	producersDone atomic.Bool
}

func startProducers(r *ring.Ring[item], wg *sync.WaitGroup, workers, perWorker int, tally *mpmcTally) {
	for p := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range perWorker {
				it := &item{n: p*perWorker + i}
				for !r.Enqueue(it) {
					runtime.Gosched()
				}

				tally.produced.Add(1)
			}
		}()
	}
}

func startConsumers(r *ring.Ring[item], wg *sync.WaitGroup, workers int, tally *mpmcTally) {
	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for !tally.producersDone.Load() || !r.IsEmpty() {
				got, ok := r.Dequeue()
				if !ok {
					runtime.Gosched()

					continue
				}

				if _, dup := tally.seen.LoadOrStore(got.n, struct{}{}); dup {
					tally.dupes.Add(1)
				}

				tally.consumed.Add(1)
			}
		}()
	}
}

func BenchmarkEnqueueDequeue(b *testing.B) {
	r := ring.New[item](1<<12, 1<<12)
	it := &item{n: 1}

	b.ReportAllocs()

	for b.Loop() {
		r.Enqueue(it)
		r.Dequeue()
	}
}

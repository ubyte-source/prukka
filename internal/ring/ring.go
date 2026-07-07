package ring

import (
	"math"
	"sync/atomic"
)

// defaultCapacity is the ring size chosen when the caller passes zero.
const defaultCapacity uint64 = 1 << 16

// clampU64ToI64 converts a uint64 counter to int64 for Metrics.
func clampU64ToI64(v uint64) int64 {
	if v > uint64(math.MaxInt64) {
		return math.MaxInt64
	}

	return int64(v)
}

// CacheLineSize is used for padding to minimize false sharing.
const CacheLineSize = 64

type padding [CacheLineSize]byte

// slotDataSize is the combined size of the active slot fields (ptr + seq).
const slotDataSize = 16

// slot pairs a pointer with a lifecycle sequence (free=pos, filled=pos+1,
// released=pos+capacity); the zero value is first-lap free. Cache-padded.
type slot[T any] struct {
	ptr atomic.Pointer[T]
	seq atomic.Uint64
	_   [CacheLineSize - slotDataSize]byte
}

// Ring is an MPMC lock-free ring buffer of typed pointers (invariants in
// doc.go); fields are hand-tuned into padded cache-line islands.
//
//nolint:govet // fieldalignment: cache-line islands are intentional for MPMC throughput; packing defeats it.
type Ring[T any] struct {
	// Producer hot path
	writeIndex                 atomic.Uint64
	enqueueCount               atomic.Uint64
	enqueueDroppedByCapacity   atomic.Uint64
	enqueueDroppedByContention atomic.Uint64
	_                          padding

	// Consumer hot path
	readIndex    atomic.Uint64
	dequeueCount atomic.Uint64
	_            padding

	// Shared / read-mostly
	buffer            []slot[T]
	capacity          uint64
	mask              uint64
	requestedCapacity uint64
}

// New rounds capacity up to a power of two and allocates at least twice the
// logical capacity, so wraps never overlap in-flight reads.
func New[T any](capacity, requestedCap uint64) *Ring[T] {
	if capacity == 0 {
		capacity = defaultCapacity
	}

	capacityPow2 := nextPowerOfTwo(capacity)
	if requestedCap == 0 || requestedCap > capacityPow2 {
		requestedCap = capacityPow2
	}

	for capacityPow2 < maxPracticalCapacity && capacityPow2 < 2*requestedCap {
		capacityPow2 *= 2
	}

	return &Ring[T]{
		buffer:            make([]slot[T], capacityPow2),
		capacity:          capacityPow2,
		mask:              capacityPow2 - 1,
		requestedCapacity: requestedCap,
	}
}

// maxPracticalCapacity caps ring size to catch configuration errors.
const maxPracticalCapacity uint64 = 1 << 40

// nextPowerOfTwo returns the next power of two >= n.
func nextPowerOfTwo(n uint64) uint64 {
	if n == 0 {
		return 1
	}

	if n >= maxPracticalCapacity {
		return maxPracticalCapacity
	}

	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32

	return n + 1
}

// Size is best-effort: the two index loads are not one snapshot, so the
// transient negative is clamped to zero.
func (r *Ring[T]) Size() uint64 {
	w := r.writeIndex.Load()
	rd := r.readIndex.Load()

	if rd >= w {
		return 0
	}

	return w - rd
}

// Capacity is the physical slot count (always a power of two).
func (r *Ring[T]) Capacity() uint64 { return r.capacity }

// IsFull is a best-effort snapshot.
func (r *Ring[T]) IsFull() bool {
	return r.Size() >= r.requestedCapacity
}

// IsEmpty is a best-effort snapshot.
func (r *Ring[T]) IsEmpty() bool {
	return r.readIndex.Load() >= r.writeIndex.Load()
}

// Enqueue publishes v; false means full or retry budget exhausted, and a
// retry after Gosched is the intended reaction.
func (r *Ring[T]) Enqueue(v *T) bool {
	const maxRetries = 128
	for range maxRetries {
		w := r.writeIndex.Load()
		rd := r.readIndex.Load()

		// rd is loaded after w and can overtake it; unsigned w-rd would then
		// underflow into a spurious "full", so require w >= rd first.
		if w >= rd && w-rd >= r.requestedCapacity {
			r.enqueueDroppedByCapacity.Add(1)

			return false
		}

		s := &r.buffer[w&r.mask]

		seq := s.seq.Load()
		if free := seq == w || (seq == 0 && w < r.capacity); !free {
			// The previous-lap consumer is inside its release window, or
			// writeIndex moved under us; both clear in a few instructions.
			continue
		}

		if !r.writeIndex.CompareAndSwap(w, w+1) {
			continue
		}

		// The CAS makes this goroutine the position's only producer, and
		// the freeness check above already proved the slot is ours.
		s.ptr.Store(v)
		s.seq.Store(w + 1)
		r.enqueueCount.Add(1)

		return true
	}

	r.enqueueDroppedByContention.Add(1)

	return false
}

// Dequeue returns (nil, false) when nothing is ready — empty, or the head
// producer is mid-publish; retry after Gosched.
func (r *Ring[T]) Dequeue() (*T, bool) {
	for {
		rd := r.readIndex.Load()
		s := &r.buffer[rd&r.mask]
		seq := s.seq.Load()

		if seq < rd+1 {
			return nil, false // head not published yet: empty
		}

		if seq > rd+1 {
			continue // another consumer advanced past rd; reload
		}

		if !r.readIndex.CompareAndSwap(rd, rd+1) {
			continue // lost the claim race; reload
		}

		// The CAS makes this goroutine the position's only consumer; the
		// slot stays untouchable by producers until the release below.
		val := s.ptr.Load()
		s.ptr.Store(nil)
		s.seq.Store(rd + r.capacity)
		r.dequeueCount.Add(1)

		return val, true
	}
}

// Metrics holds best-effort internal counters and state of a Ring.
type Metrics struct {
	Capacity            int64
	LogicalCapacity     int64
	Size                int64
	EnqueueCount        int64
	DequeueCount        int64
	DroppedByCapacity   int64
	DroppedByContention int64
	IsEmpty             bool
	IsFull              bool
}

// GetMetrics returns best-effort internal counters/state (zero allocs).
func (r *Ring[T]) GetMetrics() Metrics {
	return Metrics{
		Capacity:            clampU64ToI64(r.capacity),
		LogicalCapacity:     clampU64ToI64(r.requestedCapacity),
		Size:                clampU64ToI64(r.Size()),
		EnqueueCount:        clampU64ToI64(r.enqueueCount.Load()),
		DequeueCount:        clampU64ToI64(r.dequeueCount.Load()),
		DroppedByCapacity:   clampU64ToI64(r.enqueueDroppedByCapacity.Load()),
		DroppedByContention: clampU64ToI64(r.enqueueDroppedByContention.Load()),
		IsEmpty:             r.IsEmpty(),
		IsFull:              r.IsFull(),
	}
}

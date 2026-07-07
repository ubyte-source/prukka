package ring

import (
	"math"
	"testing"
)

// payload is a local test type for the internal (white-box) package.
type payload struct{ n int }

func TestClampU64ToI64(t *testing.T) {
	t.Parallel()

	if got := clampU64ToI64(42); got != 42 {
		t.Errorf("clampU64ToI64(42) = %d", got)
	}

	if got := clampU64ToI64(math.MaxUint64); got != math.MaxInt64 {
		t.Errorf("clampU64ToI64(MaxUint64) = %d, want clamped MaxInt64", got)
	}
}

func TestNextPowerOfTwo(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want uint64 }{
		{0, 1}, {1, 1}, {3, 4}, {5, 8},
		{1 << 16, 1 << 16},
		{maxPracticalCapacity + 1, maxPracticalCapacity},
	}

	for _, c := range cases {
		if got := nextPowerOfTwo(c.in); got != c.want {
			t.Errorf("nextPowerOfTwo(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEnqueueNeverBlocksOnUnreleasedSlot: a stalled consumer's slot makes
// Enqueue report contention, never spin forever.
func TestEnqueueNeverBlocksOnUnreleasedSlot(t *testing.T) {
	t.Parallel()

	r := New[payload](4, 4) // physical capacity rounds up to 8
	const pos = 0
	idx := pos & r.mask

	// Park the slot in a foreign state, as a stalled consumer would.
	r.buffer[idx].seq.Store(pos + 1)

	if r.Enqueue(&payload{n: 1}) {
		t.Fatal("Enqueue landed on a slot that was never released")
	}

	if r.GetMetrics().DroppedByContention != 1 {
		t.Fatalf("DroppedByContention = %d, want 1", r.GetMetrics().DroppedByContention)
	}

	// Release the slot: the retry must now succeed.
	r.buffer[idx].seq.Store(pos)

	if !r.Enqueue(&payload{n: 2}) {
		t.Fatal("Enqueue failed on a released slot")
	}

	if got, ok := r.Dequeue(); !ok || got.n != 2 {
		t.Fatalf("Dequeue = %v/%v, want the published payload", got, ok)
	}
}

// TestDequeueNeverBlocksOnUnpublishedSlot: an unpublished head makes
// Dequeue report empty, never spin.
func TestDequeueNeverBlocksOnUnpublishedSlot(t *testing.T) {
	t.Parallel()

	r := New[payload](4, 4)

	// Simulate a producer stalled inside its publish window: the position
	// is claimed (writeIndex advanced) but the slot is not yet published.
	r.writeIndex.Store(1)

	if _, ok := r.Dequeue(); ok {
		t.Fatal("Dequeue returned an item from an unpublished slot")
	}

	// The producer completes its two stores; the item must now flow.
	value := &payload{n: 7}
	r.buffer[0].ptr.Store(value)
	r.buffer[0].seq.Store(1)

	got, ok := r.Dequeue()
	if !ok || got != value {
		t.Fatalf("Dequeue = %v/%v, want the published payload", got, ok)
	}

	if _, ok := r.Dequeue(); ok {
		t.Fatal("Dequeue delivered a second item from a single publish")
	}
}

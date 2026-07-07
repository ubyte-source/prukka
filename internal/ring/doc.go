// Package ring is a lock-free MPMC ring buffer of typed pointers — the
// queue behind the provider dispatcher.
//
// Invariants: indices sit on separate cache lines; each slot's sequence
// encodes its lifecycle (free=pos, filled=pos+1, released=pos+capacity);
// a position is claimed only when its slot is already in the needed state,
// so no wait ever follows a claim; a peer mid-publish surfaces as a
// transient false and callers retry; every accepted item is delivered
// exactly once; capacity is a power of two.
package ring

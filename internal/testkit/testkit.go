// Package testkit holds the few test fixtures shared across packages.
// Production code must not import it.
package testkit

import (
	"testing"
	"time"
)

// pollPace bounds how often Eventually re-checks its condition: fast enough
// for sub-second test deadlines, slow enough not to spin.
const pollPace = 5 * time.Millisecond

// Eventually polls cond until it holds or timeout elapses, then fails the
// test with what. It replaces hand-rolled deadline/sleep loops so every
// package waits the same way.
func Eventually(tb testing.TB, timeout time.Duration, cond func() bool, what string) {
	tb.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("condition not reached within %s: %s", timeout, what)
		}
		time.Sleep(pollPace)
	}
}

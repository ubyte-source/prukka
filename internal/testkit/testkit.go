// Package testkit holds the few test fixtures shared across packages.
// Production code must not import it.
package testkit

import (
	"testing"
	"time"
)

const pollPace = 5 * time.Millisecond

// Eventually polls cond until it holds or timeout elapses, then fails the test
// with what. Use it only for waits testing/synctest cannot reach — a
// subprocess, a device, the filesystem — none of which are durably blocking;
// pure in-process concurrency belongs in synctest.Test.
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

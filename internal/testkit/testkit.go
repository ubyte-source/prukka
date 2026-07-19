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
// test with what. It is for waits testing/synctest cannot reach: a
// subprocess, ffmpeg, a device or the filesystem, none of which are durably
// blocking, so no bubble can tell when they have settled.
//
// Pure in-process concurrency belongs in synctest.Test instead, where
// synctest.Wait answers "has it settled?" exactly rather than by polling a
// wall clock. Two things to know before converting one: t.Parallel panics
// inside a bubble, so it must stay on the outer test with the bubble opened
// after it; and sync.Mutex.Lock is explicitly not durably blocking, so a
// test that waits for a goroutine to park on a lock still needs a channel
// handshake — see internal/core/config/holder_test.go.
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

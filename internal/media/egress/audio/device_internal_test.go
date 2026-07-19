package audio

import (
	"testing"
	"time"
)

// TestStallGuardSeversAWedgedWrite: a Write that makes no progress is severed
// within the stall budget instead of blocking forever.
func TestStallGuardSeversAWedgedWrite(t *testing.T) {
	shrinkStallTimeout(t)

	wedged := newBlockingSink(0)
	guard := newStallGuard(wedged)
	t.Cleanup(guard.sever)

	done := make(chan error, 1)
	go func() {
		_, err := guard.Write(make([]byte, 4))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("severed write returned nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stall guard never severed the wedged write")
	}
}

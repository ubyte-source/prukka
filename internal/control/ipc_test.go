package control_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ubyte-source/prukka/internal/control"
)

// TestErrDaemonRunningIsAStableSentinel: `prukka up` matches this value
// with errors.Is to print its friendly hint, so wrapped chains must keep
// resolving to it.
func TestErrDaemonRunningIsAStableSentinel(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("listen control socket: %w", control.ErrDaemonRunning)
	if !errors.Is(wrapped, control.ErrDaemonRunning) {
		t.Fatal("a wrapped ErrDaemonRunning no longer matches errors.Is")
	}
}

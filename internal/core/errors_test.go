package core_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

// TestErrTransientIsAStableSentinel: wrapped chains must resolve to this
// exact sentinel.
func TestErrTransientIsAStableSentinel(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("call translator: %w", core.ErrTransient)
	if !errors.Is(wrapped, core.ErrTransient) {
		t.Fatal("a wrapped ErrTransient no longer matches errors.Is")
	}

	if core.ErrTransient.Error() != "transient provider failure" {
		t.Fatalf("sentinel message changed: %q", core.ErrTransient.Error())
	}
}

func TestErrNotReadySurvivesWrapping(t *testing.T) {
	t.Parallel()

	if err := fmt.Errorf("start output: %w", core.ErrNotReady); !errors.Is(err, core.ErrNotReady) {
		t.Fatal("wrapped ErrNotReady no longer matches errors.Is")
	}
}

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

	wrapped := fmt.Errorf("call openrouter: %w", core.ErrTransient)
	if !errors.Is(wrapped, core.ErrTransient) {
		t.Fatal("a wrapped ErrTransient no longer matches errors.Is")
	}

	if core.ErrTransient.Error() != "transient provider failure" {
		t.Fatalf("sentinel message changed: %q", core.ErrTransient.Error())
	}
}

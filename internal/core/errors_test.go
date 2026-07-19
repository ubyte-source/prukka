package core_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestErrNotReadySurvivesWrapping(t *testing.T) {
	t.Parallel()

	if err := fmt.Errorf("start output: %w", core.ErrNotReady); !errors.Is(err, core.ErrNotReady) {
		t.Fatal("wrapped ErrNotReady no longer matches errors.Is")
	}
}

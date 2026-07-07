//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
)

// TestRunServiceInteractivePassThrough: from a console the daemon body
// runs directly, its error untouched.
func TestRunServiceInteractivePassThrough(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("daemon ended")

	err := runService(context.Background(), func(context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runService = %v, want the body's error", err)
	}
}

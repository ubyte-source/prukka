//go:build !windows

package main

import (
	"context"
	"errors"
	"testing"
)

// TestRunServiceIsAPassThrough: off Windows the daemon body runs directly,
// its error untouched.
func TestRunServiceIsAPassThrough(t *testing.T) {
	t.Parallel()

	type key struct{}

	sentinel := errors.New("daemon ended")
	ctx := context.WithValue(context.Background(), key{}, "v")

	err := runService(ctx, func(got context.Context) error {
		if got.Value(key{}) != "v" {
			t.Fatal("runService swapped the context")
		}

		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runService = %v, want the body's error", err)
	}
}

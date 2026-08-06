//go:build !windows

package wasapi

import "testing"

func TestOpenRefusesOffWindows(t *testing.T) {
	t.Parallel()

	w, err := Open("device://audio/default")
	if err == nil || w != nil {
		t.Fatalf("Open = (%v, %v), want a Windows-only error", w, err)
	}
}

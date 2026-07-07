//go:build !windows

package wasapi

import "testing"

// TestOpenRefusesOffWindows: a clear error so the ffmpeg fallback is a
// decision, not a nil dereference.
func TestOpenRefusesOffWindows(t *testing.T) {
	t.Parallel()

	w, err := Open("device://audio/default")
	if err == nil || w != nil {
		t.Fatalf("Open = (%v, %v), want a Windows-only error", w, err)
	}
}

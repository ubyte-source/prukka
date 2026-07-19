package hls

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

// newCue builds a LiveCue against a throwaway dir and discard logger.
func newCue(t *testing.T) *LiveCue {
	t.Helper()

	return newLiveCue(t.TempDir(), slog.New(slog.DiscardHandler))
}

// TestStaleShowNeverOverwritesNewerCue: a viewer must never see a caption
// replaced by an earlier one.
func TestStaleShowNeverOverwritesNewerCue(t *testing.T) {
	t.Parallel()

	c := newCue(t)
	defer c.Close()

	c.Schedule(&vtt.Cue{Lines: []string{"old"}, Start: 80 * time.Millisecond, End: 500 * time.Millisecond})
	c.Schedule(&vtt.Cue{Lines: []string{"new"}, Start: 0, End: 500 * time.Millisecond})

	time.Sleep(160 * time.Millisecond) // the old show timer has fired by now

	body, err := os.ReadFile(c.Path())
	if err != nil || string(body) != "new" {
		t.Fatalf("overlay = %q (%v), want the newer cue to keep the file", body, err)
	}
}

// TestStaleHideNeverClearsNewerCue: an old generation's clear must not
// erase text still inside its window.
func TestStaleHideNeverClearsNewerCue(t *testing.T) {
	t.Parallel()

	c := newCue(t)
	defer c.Close()

	c.Schedule(&vtt.Cue{Lines: []string{"old"}, Start: 0, End: 40 * time.Millisecond})
	c.Schedule(&vtt.Cue{Lines: []string{"new"}, Start: 0, End: 800 * time.Millisecond})

	time.Sleep(120 * time.Millisecond) // the old hide timer has fired by now

	body, err := os.ReadFile(c.Path())
	if err != nil || string(body) != "new" {
		t.Fatalf("overlay = %q (%v), want the newer cue untouched by the stale clear", body, err)
	}
}

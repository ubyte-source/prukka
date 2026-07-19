package hls_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// cuePath locates the live overlay file the segmenter maintains.
func cuePath(dir string) string {
	return filepath.Join(dir, "current.txt")
}

// readCue polls the overlay until it contains want or the budget elapses.
func readCue(t *testing.T, path, want string) bool {
	t.Helper()

	for range 100 {
		body, err := os.ReadFile(filepath.Clean(path))
		if err == nil && strings.Contains(string(body), want) {
			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return false
}

// waitEmpty polls the overlay until it is empty again.
func waitEmpty(t *testing.T, path string) bool {
	t.Helper()

	for range 100 {
		body, err := os.ReadFile(filepath.Clean(path))
		if err == nil && len(body) == 0 {
			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return false
}

func TestOverlayExistsEagerlyAndEmpty(t *testing.T) {
	t.Parallel()

	_, dir := newSegmenter(t)

	body, err := os.ReadFile(filepath.Clean(cuePath(dir)))
	if err != nil {
		t.Fatalf("overlay must exist before the first caption: %v", err)
	}

	if len(body) != 0 {
		t.Fatalf("initial overlay = %q, want empty", body)
	}
}

func TestOverlayShowsAndClearsOnTheWallClock(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	// The cue window starts ~instantly on the wall clock (anchor + 50ms)
	// and lasts 300ms: the text must appear, then clear.
	segmenter.Append(&core.TranslatedSegment{
		Session:    "demo",
		Target:     "en",
		Text:       "Live overlay text.",
		ScheduleAt: 50 * time.Millisecond,
		Duration:   300 * time.Millisecond,
	})

	if !readCue(t, cuePath(dir), "Live overlay text.") {
		t.Fatal("overlay never showed the cue")
	}

	if !waitEmpty(t, cuePath(dir)) {
		t.Fatal("overlay never cleared after the cue window")
	}
}

func TestOverlayExpiredCueIsDropped(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	// A cue whose window closed long before it arrived must not flash.
	segmenter.Append(&core.TranslatedSegment{
		Session:    "demo",
		Target:     "en",
		Text:       "Stale cue.",
		ScheduleAt: -10 * time.Second,
		Duration:   time.Second,
	})

	time.Sleep(50 * time.Millisecond)

	body, err := os.ReadFile(filepath.Clean(cuePath(dir)))
	if err != nil || len(body) != 0 {
		t.Fatalf("stale cue reached the overlay: %q (%v)", body, err)
	}
}

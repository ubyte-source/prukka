package hls_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/testkit"
)

func cuePath(dir string) string {
	return filepath.Join(dir, "current.txt")
}

// overlayPatience is generous on purpose: the cue windows under test are
// hundreds of milliseconds, but a shared CI runner under the race detector can
// stall well past a second.
const overlayPatience = 30 * time.Second

func readCue(t *testing.T, path, want string) {
	t.Helper()

	testkit.Eventually(t, overlayPatience, func() bool {
		body, err := os.ReadFile(filepath.Clean(path))

		return err == nil && strings.Contains(string(body), want)
	}, "overlay never showed the cue "+want)
}

func waitEmpty(t *testing.T, path string) {
	t.Helper()

	testkit.Eventually(t, overlayPatience, func() bool {
		body, err := os.ReadFile(filepath.Clean(path))

		return err == nil && len(body) == 0
	}, "overlay never cleared after the cue window")
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

	// The cue window starts at anchor + 50ms and lasts 300ms.
	segmenter.Append(&core.TranslatedSegment{
		Slug:       "demo",
		Text:       "Live overlay text.",
		ScheduleAt: 50 * time.Millisecond,
		Duration:   300 * time.Millisecond,
	})

	readCue(t, cuePath(dir), "Live overlay text.")
	waitEmpty(t, cuePath(dir))
}

func TestOverlayExpiredCueIsDropped(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	// A cue whose window closed long before it arrived must not flash.
	segmenter.Append(&core.TranslatedSegment{
		Slug:       "demo",
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

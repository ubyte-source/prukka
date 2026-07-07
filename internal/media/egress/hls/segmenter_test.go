package hls_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
)

// newSegmenter builds a segmenter through the store so the test exercises
// the real wiring; it returns the segmenter and its on-disk directory.
func newSegmenter(t *testing.T) (segmenter *hls.Segmenter, dir string) {
	t.Helper()

	store := newStore(t)

	session, err := store.Create("demo", []core.Lang{"en"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	return session.Subtitles("en"), filepath.Join(filepath.Dir(session.VideoDir()), "subs", "en")
}

func seg(text string, at, dur time.Duration) *core.TranslatedSegment {
	return &core.TranslatedSegment{
		Session:    "demo",
		Target:     "en",
		Text:       text,
		ScheduleAt: at,
		Duration:   dur,
	}
}

func TestAppendWritesPartAndPlaylist(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	segmenter.Append(seg("Hello world.", 8*time.Second, 2*time.Second))

	// 8s–10s falls in part 1 (6s parts).
	part, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "seg00001.vtt")))
	if err != nil {
		t.Fatalf("part file: %v", err)
	}

	text := string(part)

	for _, want := range []string{"WEBVTT", "X-TIMESTAMP-MAP", "00:00:08.000 --> 00:00:10.000", "Hello world."} {
		if !strings.Contains(text, want) {
			t.Errorf("part missing %q:\n%s", want, text)
		}
	}

	playlist, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "index.m3u8")))
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}

	for _, want := range []string{"#EXT-X-TARGETDURATION:6", "seg00001.vtt", "#EXT-X-MEDIA-SEQUENCE:0"} {
		if !strings.Contains(string(playlist), want) {
			t.Errorf("playlist missing %q:\n%s", want, playlist)
		}
	}
}

func TestCueSpanningPartsIsRepeated(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	// 5s–8s crosses the 6s boundary: RFC 8216 wants it in both parts.
	segmenter.Append(seg("Across the boundary.", 5*time.Second, 3*time.Second))

	for _, name := range []string{"seg00000.vtt", "seg00001.vtt"} {
		part, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if err != nil {
			t.Fatalf("part %s: %v", name, err)
		}

		if !strings.Contains(string(part), "Across the boundary.") {
			t.Errorf("part %s missing the spanning cue:\n%s", name, part)
		}
	}
}

func TestWindowRollsAndEvicts(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	segmenter.Append(seg("Early cue.", 2*time.Second, time.Second))
	// Ten parts later: the first part must leave the window and the disk.
	segmenter.Append(seg("Late cue.", 62*time.Second, time.Second))

	if _, err := os.Stat(filepath.Join(dir, "seg00000.vtt")); !os.IsNotExist(err) {
		t.Fatalf("evicted part still on disk: %v", err)
	}

	playlist, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "index.m3u8")))
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}

	text := string(playlist)

	if strings.Contains(text, "seg00000.vtt") {
		t.Fatalf("playlist still lists the evicted part:\n%s", text)
	}

	// The window is contiguous: parts between cues exist even without text.
	if !strings.Contains(text, "seg00008.vtt") {
		t.Fatalf("gap part missing from the window:\n%s", text)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "seg00008.vtt")); statErr != nil {
		t.Fatalf("gap part not written: %v", statErr)
	}

	if !strings.Contains(text, "#EXT-X-MEDIA-SEQUENCE:6") {
		t.Fatalf("media sequence must advance with the window:\n%s", text)
	}
}

func TestEmptyTextIsIgnored(t *testing.T) {
	t.Parallel()

	segmenter, dir := newSegmenter(t)

	segmenter.Append(seg("   ", time.Second, time.Second))

	if _, err := os.Stat(filepath.Join(dir, "index.m3u8")); !os.IsNotExist(err) {
		t.Fatalf("blank segment produced output: %v", err)
	}
}

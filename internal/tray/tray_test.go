package tray

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"strings"
	"testing"
)

// scriptedStats is a canned StatsSource: one result, success or failure.
type scriptedStats struct {
	err   error
	stats Stats
}

func (s scriptedStats) Stats(context.Context) (Stats, error) { return s.stats, s.err }

func TestStatusTitleRendersTheSessionCount(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := &Config{
		Stats: scriptedStats{stats: Stats{Sessions: 3}},
		Log:   slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	if got := statusTitle(context.Background(), cfg); got != "Live: 3 sessions" {
		t.Fatalf("title = %q, want the live session count", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("successful poll logged: %q", buf.String())
	}
}

// TestStatusTitleLogsThePollFailureCause: the tray is the only consumer of the
// poll error — swallowing it leaves an operator staring at "Daemon
// unreachable" with zero diagnostic anywhere.
func TestStatusTitleLogsThePollFailureCause(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := &Config{
		Stats: scriptedStats{err: errors.New("dial control socket: connection refused")},
		Log:   slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	if got := statusTitle(context.Background(), cfg); got != "Daemon unreachable" {
		t.Fatalf("title = %q, want unreachable", got)
	}
	if !strings.Contains(buf.String(), "connection refused") {
		t.Fatalf("poll failure cause was not logged: %q", buf.String())
	}
}

// TestEmbeddedIconIsAValidPNG: a broken embed would render an invisible tray
// with no error anywhere — pin each asset's contract at build time: it must
// decode, be menu-bar sized, and be alpha-shaped (an icon without transparent
// pixels renders as an opaque tile and can never serve as a macOS template).
func TestEmbeddedIconIsAValidPNG(t *testing.T) {
	t.Parallel()

	for name, data := range map[string][]byte{"icon.png": iconPNG, "icon_template.png": iconTemplatePNG} {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("embedded %s does not decode as PNG: %v", name, err)
		}

		bounds := img.Bounds()
		if bounds.Dx() != 22 || bounds.Dy() != 22 {
			t.Fatalf("embedded %s is %dx%d, want the 22x22 menu-bar size", name, bounds.Dx(), bounds.Dy())
		}
		if !hasTransparency(img) {
			t.Fatalf("embedded %s has no fully transparent pixel: the glyph must be alpha-shaped", name)
		}
	}
}

func hasTransparency(img image.Image) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha == 0 {
				return true
			}
		}
	}

	return false
}

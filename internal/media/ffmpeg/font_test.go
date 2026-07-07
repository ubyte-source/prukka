package ffmpeg

import (
	"strings"
	"testing"
)

func TestDefaultFontFileExistsOnSupportedHosts(t *testing.T) {
	t.Parallel()

	// Every supported development/CI platform ships at least one candidate.
	font := DefaultFontFile()
	if font == "" {
		t.Skip("no known system font on this host; burn-in degrades to no overlay")
	}

	if !strings.Contains(font, "/") && !strings.Contains(font, `\`) {
		t.Fatalf("font path looks wrong: %q", font)
	}
}

func TestBurnFilterEscapesPaths(t *testing.T) {
	t.Parallel()

	vf := BurnFilter(`/state/Doe, John;[live]/a:b/current.txt`, `C:\Windows\Fonts\arial.ttf`)

	for _, want := range []string{
		`drawtext=textfile=/state/Doe\, John\;\[live\]/a\:b/current.txt`,
		"reload=1",
		`fontfile=C\:\\Windows\\Fonts\\arial.ttf`,
		"fontcolor=white",
	} {
		if !strings.Contains(vf, want) {
			t.Errorf("filter missing %q:\n%s", want, vf)
		}
	}
}

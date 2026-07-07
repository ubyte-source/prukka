package vtt_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

func TestCueShapeLimits(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()
	w.Append(seg(strings.Repeat("parola essenziale ", 20), 0, 20*time.Second))

	doc := string(w.Document())

	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "WEBVTT") || strings.Contains(line, "-->") || line == "" {
			continue
		}

		if n := len([]rune(line)); n > vtt.MaxLineChars {
			t.Fatalf("line %q has %d chars, max is %d", line, n, vtt.MaxLineChars)
		}
	}

	for _, block := range strings.Split(doc, "\n\n")[1:] {
		lines := strings.Count(strings.TrimSpace(block), "\n")
		if lines > vtt.MaxLines {
			t.Fatalf("cue block has %d text lines, max is %d:\n%s", lines, vtt.MaxLines, block)
		}
	}
}

// FuzzLayout feeds arbitrary text to the cue layout: it must never panic
// and never emit a line longer than the character budget.
func FuzzLayout(f *testing.F) {
	f.Add("Buonasera a tutti.")
	f.Add(strings.Repeat("a", 200))
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		for _, cue := range vtt.Layout(text, time.Second, time.Second) {
			for _, line := range cue.Lines {
				if utf8.RuneCountInString(line) > vtt.MaxLineChars {
					t.Fatalf("line exceeds the budget: %q (%d runes)", line, utf8.RuneCountInString(line))
				}
			}
		}
	})
}

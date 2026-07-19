package vtt_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

// update regenerates golden files: go test ./media/egress/vtt/ -update
var update = flag.Bool("update", false, "rewrite golden files")

// seg builds a subtitle-only translated segment.
func seg(text string, at, dur time.Duration) *core.TranslatedSegment {
	return &core.TranslatedSegment{
		Session:    "demo",
		Track:      "main",
		Target:     "en",
		Text:       text,
		ScheduleAt: at,
		Duration:   dur,
	}
}

func TestDocumentGolden(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()

	// A short cue, a long segment spanning two cues, an unspaced word that
	// needs hard splitting, and a zero-duration segment on a nominal cue.
	w.Append(seg("Buonasera a tutti e benvenuti alla diretta di questa sera.", 8*time.Second, 3*time.Second))
	w.Append(seg("Questa è una frase molto più lunga che deve essere spezzata su più righe "+
		"e quindi su più sottotitoli consecutivi per rispettare i limiti di leggibilità.",
		11*time.Second, 8*time.Second))
	w.Append(seg(strings.Repeat("库", 50), 19*time.Second, 4*time.Second))
	w.Append(seg("Senza durata.", 23*time.Second, 0))

	got := w.Document()

	if *update {
		if err := os.WriteFile(filepath.Join("testdata", "rolling.vtt"), got, 0o600); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}

	want, err := os.ReadFile(filepath.Join("testdata", "rolling.vtt"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("document differs from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

}

// TestCueTextIsEscaped: a cue must escape the WebVTT metacharacters so
// translated text with & or angle brackets renders literally.
func TestCueTextIsEscaped(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()
	w.Append(seg("Tom & <Jerry>", time.Second, time.Second))

	doc := string(w.Document())

	if !strings.Contains(doc, "Tom &amp; &lt;Jerry&gt;") {
		t.Fatalf("cue markup not escaped:\n%s", doc)
	}
}

func TestCuesTileTheSegment(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()
	w.Append(seg(strings.Repeat("uno due tre quattro cinque sei ", 8), 10*time.Second, 6*time.Second))

	doc := string(w.Document())

	if !strings.Contains(doc, "00:00:10.000 --> ") {
		t.Fatalf("first cue does not start at the segment PTS:\n%s", doc)
	}

	if !strings.Contains(doc, " --> 00:00:16.000") {
		t.Fatalf("last cue does not end at PTS+duration:\n%s", doc)
	}
}

func TestRollingEviction(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()

	for i := range 220 {
		w.Append(seg(fmt.Sprintf("riga %d", i), time.Duration(i)*time.Second, time.Second))
	}

	doc := string(w.Document())

	if strings.Contains(doc, "riga 0\n") {
		t.Fatal("oldest cue survived past the roll limit")
	}

	if !strings.Contains(doc, "riga 219") {
		t.Fatal("newest cue missing from the document")
	}

}

func TestEmptyDocumentAndEmptyText(t *testing.T) {
	t.Parallel()

	w := vtt.NewWriter()
	w.Append(seg("   ", 0, time.Second))

	if got := string(w.Document()); got != "WEBVTT\n" {
		t.Fatalf("empty document = %q, want bare header", got)
	}
}

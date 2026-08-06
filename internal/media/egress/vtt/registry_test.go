package vtt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

func TestRegistryRoundTrip(t *testing.T) {
	t.Parallel()

	r := vtt.NewRegistry()
	w := r.Create("demo", "en")

	w.Append(&core.TranslatedSegment{
		Slug:     "demo",
		Text:     "Hello there.",
		Duration: time.Second,
	})

	doc, ok := r.Document("demo", "en")
	if !ok || !strings.Contains(string(doc), "Hello there.") {
		t.Fatalf("Document(demo,en) = (%q, %v), want the appended cue", doc, ok)
	}

	if _, ok := r.Document("demo", "de"); ok {
		t.Fatal("an unregistered language served a document")
	}

	if _, ok := r.Document("ghost", "en"); ok {
		t.Fatal("an unknown session served a document")
	}

	r.Drop("demo")

	if _, ok := r.Document("demo", "en"); ok {
		t.Fatal("a dropped session still serves documents")
	}
}

func TestDropTakesOnlyItsOwnSession(t *testing.T) {
	t.Parallel()

	r := vtt.NewRegistry()
	r.Create("demo", "en")
	r.Create("demo-live", "en")
	r.Create("demo/en", "en")

	r.Drop("demo")

	for _, slug := range []string{"demo-live", "demo/en"} {
		if _, ok := r.Document(slug, "en"); !ok {
			t.Errorf("Drop(demo) also removed session %q", slug)
		}
	}
}

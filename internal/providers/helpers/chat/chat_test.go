package chat_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/chat"
)

// system builds the prompt toward English and returns the system turn's
// content.
func system(t *testing.T, tr core.Transcript, o core.MTOpts) string {
	t.Helper()

	msgs := chat.BuildMessages(&tr, "en", &o)
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v, want a system+user pair", msgs)
	}

	return msgs[0].Content
}

func TestBuildMessagesSpellsLanguagesOut(t *testing.T) {
	t.Parallel()

	sys := system(t, core.Transcript{Lang: "it", Text: "ciao"}, core.MTOpts{})
	if !strings.Contains(sys, "Italian") || !strings.Contains(sys, "English") {
		t.Fatalf("system prompt = %q, want spelled-out languages", sys)
	}

	// The target language flows through, not just the source.
	msgs := chat.BuildMessages(&core.Transcript{Lang: "it", Text: "ciao"}, "de", &core.MTOpts{})
	if !strings.Contains(msgs[0].Content, "German") {
		t.Fatalf("system prompt = %q, want the German target spelled out", msgs[0].Content)
	}
}

func TestBuildMessagesHandlesAutoSource(t *testing.T) {
	t.Parallel()

	sys := system(t, core.Transcript{Lang: core.LangAuto, Text: "hej"}, core.MTOpts{})
	if !strings.Contains(sys, "the detected source language") {
		t.Fatalf("system prompt = %q, want the auto-detect wording", sys)
	}
}

func TestBuildMessagesCarriesUserText(t *testing.T) {
	t.Parallel()

	msgs := chat.BuildMessages(&core.Transcript{Lang: "it", Text: "buongiorno"}, "en", &core.MTOpts{})
	if msgs[1].Content != "buongiorno" {
		t.Fatalf("user turn = %q, want the transcript text", msgs[1].Content)
	}
}

func TestBuildMessagesRendersOptions(t *testing.T) {
	t.Parallel()

	sys := system(t, core.Transcript{Lang: "it", Text: "ciao"}, core.MTOpts{
		Formality: "formal",
		MinRatio:  0.8,
		MaxRatio:  1.2,
		Context:   []string{"previous line"},
	})

	for _, want := range []string{"Formality: formal.", "between 80% and 120%", "previous line"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt = %q, want it to contain %q", sys, want)
		}
	}
}

func TestBuildMessagesSortsGlossaryDeterministically(t *testing.T) {
	t.Parallel()

	glossary := map[string]string{"zebra": "zebra", "alpha": "alfa", "mid": "medio"}

	first := system(t, core.Transcript{Lang: "it", Text: "x"}, core.MTOpts{Glossary: glossary})
	if !strings.Contains(first, "alpha = alfa; mid = medio; zebra = zebra") {
		t.Fatalf("glossary rendering = %q, want sorted terms", first)
	}

	for range 5 {
		if again := system(t, core.Transcript{Lang: "it", Text: "x"}, core.MTOpts{Glossary: glossary}); again != first {
			t.Fatal("glossary rendering changed across runs; must be deterministic")
		}
	}
}

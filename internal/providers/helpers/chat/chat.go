// Package chat renders the chat-completions translation prompt shared by
// the hosted and local backends: one instruction set, one place to fix.
package chat

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
)

// Message is one chat-completions turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// BuildMessages renders the translation instruction. Languages are spelled
// out via the registry: a bare tag like "it" reads as an English pronoun
// and models return the input unchanged.
func BuildMessages(t *core.Transcript, to core.Lang, o *core.MTOpts) []Message {
	src := "the detected source language"
	if t.Lang != core.LangAuto {
		src = lang.Describe(t.Lang)
	}

	lines := []string{
		"You are a professional real-time translator for live subtitles and dubbing.",
		fmt.Sprintf("Translate from %s into %s.", src, lang.Describe(to)),
		"Output only the translation — no quotes, notes or explanations.",
		"Preserve numbers, units and proper names exactly.",
	}

	if o.Formality != "" {
		lines = append(lines, "Formality: "+o.Formality+".")
	}

	if o.MaxRatio > 0 {
		lines = append(lines, fmt.Sprintf(
			"Keep the translation length between %.0f%% and %.0f%% of the source length.",
			o.MinRatio*100, o.MaxRatio*100))
	}

	if len(o.Glossary) > 0 {
		terms := make([]string, 0, len(o.Glossary))
		for _, source := range slices.Sorted(maps.Keys(o.Glossary)) {
			terms = append(terms, source+" = "+o.Glossary[source])
		}

		lines = append(lines, "Mandatory terminology: "+strings.Join(terms, "; ")+".")
	}

	if len(o.Context) > 0 {
		lines = append(lines, "Preceding lines, for context only — do not translate them:")
		lines = append(lines, o.Context...)
	}

	return []Message{
		{Role: "system", Content: strings.Join(lines, "\n")},
		{Role: "user", Content: t.Text},
	}
}

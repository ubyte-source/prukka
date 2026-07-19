// Package lang is the single language registry: it feeds the dropdowns and
// validates every tag entering the system.
package lang

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
)

// Auto is the input sentinel — first entry of the source-language dropdown
// and accepted by the CLI — that selects source-language auto-detection.
const Auto = "auto"

// ErrUnknown marks input that does not resolve to a registered language.
var ErrUnknown = errors.New("unknown language")

// Language is one selectable entry of the registry.
type Language struct {
	Tag    core.Lang // canonical base tag, e.g. "it"
	Name   string    // English name, e.g. "Italian"
	Native string    // native name shown in dropdowns, e.g. "Italiano"
}

// Label renders the dropdown label, e.g. "Italiano — it".
func (l Language) Label() string {
	return l.Native + " — " + string(l.Tag)
}

// Suggestion pairs a candidate tag with a human-readable name for the
// "did you mean" hint.
type Suggestion struct {
	Tag  core.Lang
	Name string
}

// All returns every registered language in stable English-name order for
// dropdown rendering. The returned slice is a copy the caller may retain.
func All() []Language {
	out := make([]Language, len(registry))
	copy(out, registry)

	return out
}

// Parse validates input as a BCP-47 tag ("it", "de-CH"), normalizing case
// and separators; failures wrap ErrUnknown with a "did you mean" hint.
func Parse(input string) (core.Lang, error) {
	trimmed := strings.TrimSpace(input)
	if strings.EqualFold(trimmed, Auto) {
		return core.LangAuto, nil
	}

	base, region, err := split(trimmed)
	if err != nil {
		return "", err
	}

	entry, ok := lookup(base)
	if !ok {
		return "", unknownError(trimmed)
	}

	if region == "" {
		return entry.Tag, nil
	}

	return core.Lang(string(entry.Tag) + "-" + region), nil
}

// ParseList validates a comma-separated tag list, skipping empties and
// dropping duplicates.
func ParseList(csv string) ([]core.Lang, error) {
	items := strings.Split(csv, ",")
	out := make([]core.Lang, 0, len(items))
	seen := make(map[core.Lang]bool, len(items))

	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}

		tag, err := Parse(item)
		if err != nil {
			return nil, err
		}
		if tag == core.LangAuto {
			return nil, fmt.Errorf("%w %q: auto is valid only for a source language", ErrUnknown, item)
		}

		if !seen[tag] {
			seen[tag] = true

			out = append(out, tag)
		}
	}

	return out, nil
}

// split normalizes separators and case, returning the lowercase base subtag
// and the uppercase region subtag (empty when absent).
func split(input string) (base, region string, err error) {
	if input == "" {
		return "", "", fmt.Errorf("%w: empty tag", ErrUnknown)
	}

	parts := strings.Split(strings.ReplaceAll(input, "_", "-"), "-")
	if len(parts) > 2 {
		return "", "", fmt.Errorf("%w %q: at most one region subtag is supported", ErrUnknown, input)
	}

	base = strings.ToLower(parts[0])
	if !alpha2(base) {
		return "", "", unknownError(input)
	}

	if len(parts) == 2 {
		region = strings.ToUpper(parts[1])
		if !alpha2(region) {
			return "", "", fmt.Errorf("%w %q: invalid region subtag %q", ErrUnknown, input, parts[1])
		}
	}

	return base, region, nil
}

// alpha2 reports whether s is exactly two ASCII letters.
func alpha2(s string) bool {
	if len(s) != 2 {
		return false
	}

	for _, r := range s {
		lower := r | 0x20 // ASCII case fold
		if lower < 'a' || lower > 'z' {
			return false
		}
	}

	return true
}

// lookup scans the registry for a base tag; linear is fine off the hot
// path.
func lookup(base string) (Language, bool) {
	for _, l := range registry {
		if string(l.Tag) == base {
			return l, true
		}
	}

	return Language{}, false
}

// unknownError builds the error, attaching a hint when the input is
// a recognized confusion (usually a country code) or a spelled-out name.
func unknownError(input string) error {
	suggestions := suggestionsFor(strings.ToLower(input))
	if len(suggestions) == 0 {
		return fmt.Errorf("%w %q", ErrUnknown, input)
	}

	hints := make([]string, len(suggestions))
	for i, s := range suggestions {
		hints[i] = fmt.Sprintf("%q (%s)", string(s.Tag), s.Name)
	}

	return fmt.Errorf("%w %q — did you mean %s?", ErrUnknown, input, strings.Join(hints, " or "))
}

// suggestionsFor resolves a lowercase input to likely intended languages.
func suggestionsFor(input string) []Suggestion {
	if s, ok := confusions[input]; ok {
		return s
	}

	for _, l := range registry {
		if strings.EqualFold(l.Name, input) || strings.EqualFold(l.Native, input) {
			return []Suggestion{{Tag: l.Tag, Name: l.Name}}
		}
	}

	return nil
}

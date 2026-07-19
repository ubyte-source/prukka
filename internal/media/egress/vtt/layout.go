package vtt

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Layout wraps text into cues of at most MaxLines × MaxLineChars, splitting
// the segment duration across cues proportionally to their character share.
func Layout(text string, start, dur time.Duration) []Cue {
	lines := wrap(text, MaxLineChars)

	groups := make([][]string, 0, (len(lines)+MaxLines-1)/MaxLines)
	for i := 0; i < len(lines); i += MaxLines {
		groups = append(groups, lines[i:min(i+MaxLines, len(lines))])
	}

	total := 0
	for _, line := range lines {
		total += utf8.RuneCountInString(line)
	}

	cues := make([]Cue, 0, len(groups))
	cueStart := start

	for i, group := range groups {
		chars := 0
		for _, line := range group {
			chars += utf8.RuneCountInString(line)
		}

		end := cueStart + time.Duration(float64(dur)*float64(chars)/float64(total))
		if i == len(groups)-1 {
			// The last cue absorbs rounding drift so cues exactly tile the
			// segment.
			end = start + dur
		}

		cues = append(cues, Cue{Lines: group, Start: cueStart, End: end})
		cueStart = end
	}

	return cues
}

// wrap breaks text into lines of at most width characters, preferring word
// boundaries and hard-splitting words longer than a full line.
func wrap(text string, width int) []string {
	words := strings.Fields(text)

	lines := make([]string, 0, len(words)/2+1)
	current := ""

	for _, word := range words {
		for _, piece := range split(word, width) {
			switch {
			case current == "":
				current = piece
			case utf8.RuneCountInString(current)+1+utf8.RuneCountInString(piece) <= width:
				current += " " + piece
			default:
				lines = append(lines, current)
				current = piece
			}
		}
	}

	if current != "" {
		lines = append(lines, current)
	}

	return lines
}

// split hard-cuts a single word into width-sized rune chunks; almost every
// word returns unchanged.
func split(word string, width int) []string {
	if utf8.RuneCountInString(word) <= width {
		return []string{word}
	}

	runes := []rune(word)

	out := make([]string, 0, len(runes)/width+1)
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}

	return append(out, string(runes))
}

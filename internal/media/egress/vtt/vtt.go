// Package vtt renders live rolling WebVTT: cues on the source clock plus
// delay D, wrapped to 2×42 characters.
package vtt

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// MaxLineChars is the per-line character budget.
const MaxLineChars = 42

// MaxLines is the per-cue line budget.
const MaxLines = 2

// maxCues bounds the rolling document; players re-fetch the file, so ten
// minutes of typical cue density is plenty.
const maxCues = 200

// nominalCueDuration times cues whose segment carries no duration; such
// segments also count as QA warnings because their timing is guessed.
const nominalCueDuration = time.Second

// Cue is one rendered subtitle.
type Cue struct {
	Lines []string
	Start time.Duration
	End   time.Duration
}

// Writer accumulates one pair's cues and renders the rolling document;
// safe for concurrent use.
type Writer struct {
	cues []Cue
	mu   sync.Mutex
}

// NewWriter returns an empty document.
func NewWriter() *Writer {
	return &Writer{cues: make([]Cue, 0, maxCues)}
}

// Append converts one segment into cues; long text splits proportionally
// to character counts.
func (w *Writer) Append(seg *core.TranslatedSegment) {
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return
	}

	start := seg.ScheduleAt

	dur := seg.Duration
	if dur <= 0 {
		dur = nominalCueDuration
	}

	cues := Layout(text, start, dur)

	w.mu.Lock()
	defer w.mu.Unlock()

	for i := range cues {
		w.push(&cues[i])
	}
}

// push appends one cue under the lock, evicting the oldest past the roll
// limit.
func (w *Writer) push(c *Cue) {
	if len(w.cues) == maxCues {
		w.cues = append(w.cues[:0], w.cues[1:]...)
	}

	w.cues = append(w.cues, *c)
}

// Document renders the rolling WebVTT file.
func (w *Writer) Document() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	var sb strings.Builder

	sb.WriteString("WEBVTT\n")

	for i := range w.cues {
		c := &w.cues[i]

		sb.WriteString("\n")
		sb.WriteString(Timestamp(c.Start))
		sb.WriteString(" --> ")
		sb.WriteString(Timestamp(c.End))
		sb.WriteString("\n")
		sb.WriteString(cueText(c))
		sb.WriteString("\n")
	}

	return []byte(sb.String())
}

// cueText joins a cue's lines, escaping the WebVTT cue metacharacters.
func cueText(c *Cue) string {
	return escapeCue(strings.Join(c.Lines, "\n"))
}

// escapeCue escapes the three characters that are markup in a WebVTT cue
// payload, so translated text with & or angle brackets renders literally.
func escapeCue(text string) string {
	return cueEscaper.Replace(text)
}

// cueEscaper replaces the WebVTT cue metacharacters.
var cueEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Timestamp renders the WebVTT HH:MM:SS.mmm form.
func Timestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}

	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	ms := (d % time.Second) / time.Millisecond

	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

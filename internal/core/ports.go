// Package core holds the domain model and the port interfaces; adapters
// depend on core, never the reverse.
package core

import (
	"context"
	"strings"
	"time"
)

// Lang is a validated BCP-47 tag ("it", "de-CH"); construct from user
// input with core/lang, code past that boundary trusts it.
type Lang string

// LangAuto asks providers to detect the source language instead of pinning it.
const LangAuto Lang = ""

// Base returns the ISO 639-1 base of a language tag: "en" for "en-US".
func (l Lang) Base() Lang {
	base, _, _ := strings.Cut(string(l), "-")

	return Lang(base)
}

// SameLang reports whether two tags share a base language, case-insensitively:
// "en", "en-US" and "EN-GB" all match.
func SameLang(a, b Lang) bool {
	return strings.EqualFold(string(a.Base()), string(b.Base()))
}

// PCM is a chunk of interleaved 16-bit samples. Buffers are pooled:
// consumers must not retain Data without copying it.
type PCM struct {
	Data []int16       // interleaved samples
	Rate int           // sample rate in Hz; 16000 is the internal reference
	Ch   int           // channel count; 1 is the internal reference
	PTS  time.Duration // source-clock timestamp of the first sample
}

// Voice selects a TTS voice and declares the language its model supports.
// An empty Lang is reserved for adapters whose voices are multilingual.
type Voice struct {
	ID   string
	Lang Lang
}

// Supports reports whether the voice may synthesize the target language.
// Regional variants share the capability of their BCP-47 base language.
func (v Voice) Supports(target Lang) bool {
	if v.Lang == LangAuto {
		return true
	}

	return SameLang(v.Lang, target)
}

// TranslatedSegment is one caption and its source-clock schedule. Dubbed PCM
// is written directly to its target timeline rather than retained here.
type TranslatedSegment struct {
	Session    string
	Track      string
	Target     Lang
	Text       string        // subtitle text
	ScheduleAt time.Duration // source PTS plus the per-session delay D
	Duration   time.Duration // source utterance duration
}

// SourceSpec identifies a media source to ingest.
type SourceSpec struct {
	// URL locates the source: rtmp://, srt://, file:// or device://.
	URL string
	// VideoDir, when set, receives the passthrough HLS video rendition;
	// empty disables the tap.
	VideoDir string
	// Delay is the session delay D, shifting video timestamps onto the
	// shared output clock.
	Delay time.Duration
}

// Frames delivers labeled PCM from an opened source.
type Frames interface {
	// Next blocks for the next frame; it returns io.EOF once the source ends.
	Next(ctx context.Context) (PCM, error)
}

// Ingress opens a media source and turns it into PCM frames.
type Ingress interface {
	Open(ctx context.Context, src SourceSpec) (Frames, error)
}

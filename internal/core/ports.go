// Package core holds the domain model and the port interfaces; adapters
// depend on core, never the reverse.
package core

import (
	"context"
	"time"
)

// Lang is a validated BCP-47 tag ("it", "de-CH"); construct from user
// input with core/lang, code past that boundary trusts it.
type Lang string

// LangAuto asks providers to detect the source language instead of pinning it.
const LangAuto Lang = ""

// PCM is a chunk of interleaved 16-bit samples. Buffers are pooled:
// consumers must not retain Data without copying it.
type PCM struct {
	Data []int16       // interleaved samples
	Rate int           // sample rate in Hz; 16000 is the internal reference
	Ch   int           // channel count; 1 is the internal reference
	PTS  time.Duration // source-clock timestamp of the first sample
}

// Utterance is a VAD-endpointed span of speech attributed to one Track.
type Utterance struct {
	Session string // owning session slug
	Track   string // speaker or participant identity; the anti-loop key
	Audio   PCM
	Final   bool // set once the VAD has endpointed the utterance
}

// Transcript is the speech-to-text result for one utterance.
type Transcript struct {
	Text string
	Lang Lang
	Conf float64          // provider confidence in [0, 1]
	Span [2]time.Duration // start and end offsets on the source clock
}

// Voice selects a TTS voice. Ref, when set, is a reference sample of the
// speaker's own audio for timbre cloning; non-cloning providers ignore it.
type Voice struct {
	ID     string
	Gender string
	Ref    []int16
}

// TranslatedSegment is the engine's output unit: subtitle text plus, when
// dubbing, audio scheduled on the source clock.
type TranslatedSegment struct {
	Session    string
	Track      string
	Target     Lang
	Text       string        // subtitle text
	Audio      PCM           // dubbed audio; zero value when subtitles-only
	ScheduleAt time.Duration // source PTS plus the per-session delay D
	Duration   time.Duration // source utterance duration
	// Speaker is the stable per-stream speaker index from pitch clustering;
	// subtitles color speakers by it.
	Speaker int
}

// MTOpts tunes a single machine-translation call.
type MTOpts struct {
	Glossary  map[string]string // source term to mandated target term
	Formality string            // provider formality hint; empty selects the provider default
	Context   []string          // previous segments, for coherent incremental translation
	MaxRatio  float64           // upper bound on target/source length for isochrony
	MinRatio  float64           // lower bound on target/source length
}

// STT transcribes one utterance; the hint may be LangAuto. Implementations
// must not mutate or retain the utterance.
type STT interface {
	Transcribe(ctx context.Context, u *Utterance, hint Lang) (Transcript, error)
}

// MT translates a transcript into the target language.
type MT interface {
	Translate(ctx context.Context, t Transcript, to Lang, o MTOpts) (string, error)
}

// TTS synthesizes text into PCM audio using the given voice.
type TTS interface {
	Speak(ctx context.Context, text string, to Lang, v Voice) (PCM, error)
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

// VAD turns frames into endpointed utterances; Flush endpoints buffered
// speech at source end so the tail is never lost.
type VAD interface {
	Feed(frame PCM) []Utterance
	Flush() []Utterance
}

// Meter records provider usage and cost: every provider call
// reports the units it consumed and the euros they cost.
type Meter interface {
	Add(session, kind string, units, eur float64)
}

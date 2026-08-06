package realtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Transcript is one update from a streaming transcription of the current
// segment: Stable marks Text as committed, Final also closes the segment and
// is always Stable, and SourceEnd — valid only when HasSourceEnd — is the
// source-clock instant this update covers.
type Transcript struct {
	Text         string
	Lang         core.Lang
	SourceEnd    time.Duration
	Stable       bool
	Final        bool
	HasSourceEnd bool
}

// Segment is a committed, stable unit of source speech; one translation is
// produced per segment, and the lane fills Span from the source clock.
type Segment struct {
	Text string
	Lang core.Lang
	Span [2]time.Duration
}

// Transcriber opens a streaming transcription for one audio track.
type Transcriber interface {
	Open(ctx context.Context, hint core.Lang) (Transcription, error)
}

// Transcription is a live transcription session: push audio as it flows, read
// transcript updates until the session ends.
type Transcription interface {
	// Push submits one audio chunk with bounded backpressure.
	Push(ctx context.Context, frame core.PCM) error
	// Events streams transcript updates; the adapter closes the channel.
	Events() <-chan Transcript
	// Err reports the terminal wire or helper error after Events closes; a
	// clean EOF and context cancellation return nil.
	Err() error
	// CloseSend signals end of audio; buffered finals still arrive on Events.
	CloseSend() error
	// Close stops the session, waits for its wire and helper, and is idempotent.
	Close() error
}

// Closer releases a lane-scoped provider, waits for its in-flight helpers and
// is idempotent.
type Closer interface {
	Close() error
}

// Translator produces the target-language translation of one committed source
// segment.
type Translator interface {
	Closer
	// Supports reports whether a concrete source-to-target model is installed.
	Supports(from, to core.Lang) bool
	Translate(ctx context.Context, source Segment, to core.Lang) (string, error)
}

// LanguagePair is one directed translation capability.
type LanguagePair struct {
	From core.Lang
	To   core.Lang
}

// AudioStream couples synthesized PCM with the provider's terminal result: as
// with rows.Err, a closed audio channel alone is not evidence of success.
type AudioStream struct {
	audio   <-chan core.PCM
	result  <-chan error
	errOnce func() error
}

// NewAudioStream builds a synthesis result from an audio channel and a result
// channel that sends exactly one value after production stops.
func NewAudioStream(audio <-chan core.PCM, result <-chan error) *AudioStream {
	s := &AudioStream{audio: audio, result: result}
	s.errOnce = sync.OnceValue(s.awaitResult)

	return s
}

var (
	errNoTerminalResult    = errors.New("synthesis stream has no terminal result")
	errResultChannelClosed = errors.New("synthesis stream closed without a terminal result")
)

// Audio returns the synthesized chunks; the provider closes it at end of turn.
func (s *AudioStream) Audio() <-chan core.PCM {
	return s.audio
}

// Err waits for and caches the terminal synthesis result; call it after Audio
// closes.
func (s *AudioStream) Err() error { return s.errOnce() }

func (s *AudioStream) awaitResult() error {
	if s.result == nil {
		return errNoTerminalResult
	}
	result, ok := <-s.result
	if !ok {
		return errResultChannelClosed
	}

	return result
}

// Synthesizer streams synthesized speech for one turn: text clauses in, PCM
// chunks out with prosody continuous across the turn.
type Synthesizer interface {
	Closer
	Speak(ctx context.Context, to core.Lang, v core.Voice, text <-chan string) (*AudioStream, error)
}

package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Transcript is one update from a streaming transcription of the current
// segment. Stable marks Text as committed — it will not change, so the engine
// may translate the grown prefix (wait-k). A non-stable update refines the
// in-progress tail for live display only. Final closes the segment: the
// caption is committed and the synthesis turn ends. A Final update is always
// Stable. Timing is the engine's to assign — the transcription adapter
// reports only text and its stability.
type Transcript struct {
	Text   string
	Lang   core.Lang
	Stable bool
	Final  bool
}

// Segment is a committed, stable unit of source speech the engine hands to
// captioning and synthesis: one coherent translation is produced per segment.
// The engine fills Span from the source clock.
type Segment struct {
	Text string
	Lang core.Lang
	Span [2]time.Duration
}

// Transcriber opens a streaming transcription for one audio track. The adapter
// owns the wire and the events channel; the engine owns the audio it pushes
// and decides when the audio ends.
type Transcriber interface {
	Open(ctx context.Context, hint core.Lang) (Transcription, error)
}

// Transcription is a live transcription session: push audio as it flows, read
// transcript updates until the session ends.
type Transcription interface {
	// Push submits one audio chunk. It applies bounded backpressure and
	// returns an error once the session is closed or its context is done.
	Push(frame core.PCM) error
	// Events streams transcript updates. The adapter closes the channel when
	// the wire ends, after CloseSend drains, or when the context is canceled.
	Events() <-chan Transcript
	// Err reports the terminal wire or helper error after Events closes. A
	// clean EOF and context cancellation return nil; cancellation is reported
	// by the context that owns the transcription.
	Err() error
	// CloseSend signals end of audio; buffered finals still arrive on Events.
	CloseSend() error
	// Close stops the session and waits until its wire and helper process have
	// been released. It is idempotent.
	Close() error
}

// Closer releases a lane-scoped provider and waits for its in-flight helpers.
// Close is safe to call more than once; wrappers preserve this contract.
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

// AudioStream couples synthesized PCM with the provider's terminal result.
// Consumers drain Audio, then call Err exactly as they would for rows.Err:
// a closed audio channel alone is not evidence that inference succeeded.
type AudioStream struct {
	err    error
	audio  <-chan core.PCM
	result <-chan error
	once   sync.Once
}

// NewAudioStream builds a synthesis result from an audio channel and a result
// channel that sends exactly one value after production stops.
func NewAudioStream(audio <-chan core.PCM, result <-chan error) *AudioStream {
	return &AudioStream{audio: audio, result: result}
}

// Audio returns the synthesized chunks. The provider closes it at end of turn.
func (s *AudioStream) Audio() <-chan core.PCM {
	if s == nil {
		return nil
	}

	return s.audio
}

// Err waits for and caches the terminal synthesis result. Call it after Audio
// closes; repeated calls return the same value.
func (s *AudioStream) Err() error {
	if s == nil {
		return errors.New("nil synthesis stream")
	}

	s.once.Do(func() {
		if s.result == nil {
			s.err = errors.New("synthesis stream has no terminal result")

			return
		}
		result, ok := <-s.result
		if !ok {
			s.err = errors.New("synthesis stream closed without a terminal result")

			return
		}
		s.err = result
	})

	return s.err
}

// Synthesizer streams synthesized speech for one turn. Text clauses arrive on
// text — from incremental translation — and PCM chunks leave on the returned
// channel with prosody continuous across the turn. The adapter closes the
// audio channel when the turn's audio ends or the context is canceled.
type Synthesizer interface {
	Closer
	Speak(ctx context.Context, to core.Lang, v core.Voice, text <-chan string) (*AudioStream, error)
}

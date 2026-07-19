// Package bounded applies daemon-wide concurrency and backpressure limits to
// speech-provider calls without coupling the streaming engine to a scheduler.
package bounded

import (
	"context"
	"errors"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/dispatch"
)

// ErrClosed reports a call submitted after a lane released its provider.
var ErrClosed = errors.New("bounded provider is closed")

var errNilAudioStream = errors.New("synthesizer returned a nil audio stream")

type lifecycle struct {
	closeErr  error
	mu        sync.Mutex
	tasks     sync.WaitGroup
	closeOnce sync.Once
	closed    bool
}

func (l *lifecycle) submit(ctx context.Context, pool *dispatch.Pool, fn func()) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()

		return ErrClosed
	}
	l.tasks.Add(1)
	l.mu.Unlock()

	err := pool.Submit(ctx, func() {
		defer l.tasks.Done()
		fn()
	})
	if err != nil {
		l.tasks.Done()
	}

	return err
}

func (l *lifecycle) close(next engine.Closer) error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()

		l.closeErr = next.Close()
		l.tasks.Wait()
	})

	return l.closeErr
}

// Translator schedules machine-translation calls on a shared worker pool.
type Translator struct {
	next engine.Translator
	pool *dispatch.Pool
	life lifecycle
}

// NewTranslator bounds next with pool.
func NewTranslator(pool *dispatch.Pool, next engine.Translator) *Translator {
	return &Translator{pool: pool, next: next}
}

// Supports delegates the immutable capability query without consuming a
// worker slot.
func (t *Translator) Supports(from, to core.Lang) bool { return t.next.Supports(from, to) }

type translationResult struct {
	err  error
	text string
}

// Translate implements engine.Translator.
func (t *Translator) Translate(
	ctx context.Context, source engine.Segment, to core.Lang,
) (string, error) {
	result := make(chan translationResult, 1)
	if err := t.life.submit(ctx, t.pool, func() {
		text, translateErr := t.next.Translate(ctx, source, to)
		result <- translationResult{text: text, err: translateErr}
	}); err != nil {
		return "", err
	}

	select {
	case translated := <-result:
		return translated.text, translated.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close releases the wrapped lane-scoped provider and waits for accepted tasks.
func (t *Translator) Close() error { return t.life.close(t.next) }

// Synthesizer schedules whole synthesis turns on a shared worker pool. A
// worker remains assigned while the provider's stream is drained, so the
// configured limit bounds actual inference rather than only process startup.
type Synthesizer struct {
	next engine.Synthesizer
	pool *dispatch.Pool
	life lifecycle
}

// NewSynthesizer bounds next with pool.
func NewSynthesizer(pool *dispatch.Pool, next engine.Synthesizer) *Synthesizer {
	return &Synthesizer{pool: pool, next: next}
}

type synthesisStart struct {
	err error
}

// Speak implements engine.Synthesizer.
func (s *Synthesizer) Speak(
	ctx context.Context, to core.Lang, voice core.Voice, text <-chan string,
) (*engine.AudioStream, error) {
	output := make(chan core.PCM)
	result := make(chan error, 1)
	started := make(chan synthesisStart, 1)

	if err := s.life.submit(ctx, s.pool, func() {
		audio, speakErr := s.next.Speak(ctx, to, voice, text)
		var input <-chan core.PCM
		if speakErr == nil {
			if audio == nil {
				speakErr = errNilAudioStream
			} else if input = audio.Audio(); input == nil {
				speakErr = errNilAudioStream
			}
		}
		started <- synthesisStart{err: speakErr}
		if speakErr != nil {
			close(output)

			return
		}

		forwardErr := forward(ctx, input, output)
		result <- errors.Join(forwardErr, audio.Err())
		close(result)
	}); err != nil {
		close(output)

		return nil, err
	}

	select {
	case start := <-started:
		if start.err != nil {
			return nil, start.err
		}

		return engine.NewAudioStream(output, result), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close releases the wrapped lane-scoped provider and waits for accepted tasks.
func (s *Synthesizer) Close() error { return s.life.close(s.next) }

func forward(ctx context.Context, input <-chan core.PCM, output chan<- core.PCM) error {
	defer close(output)

	for {
		select {
		case chunk, ok := <-input:
			if !ok {
				return nil
			}

			select {
			case output <- chunk:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

var (
	_ engine.Translator  = (*Translator)(nil)
	_ engine.Synthesizer = (*Synthesizer)(nil)
	_ engine.Closer      = (*Translator)(nil)
	_ engine.Closer      = (*Synthesizer)(nil)
)

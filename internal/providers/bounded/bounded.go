// Package bounded applies daemon-wide concurrency and backpressure limits to
// speech-provider calls without coupling the streaming lane to a scheduler.
package bounded

import (
	"context"
	"errors"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/dispatch"
)

// ErrClosed reports a call submitted after a lane released its provider.
var ErrClosed = errors.New("bounded provider is closed")

var errNilAudioStream = errors.New("synthesizer returned a nil audio stream")

// lifecycle admits calls until the wrapped provider is released, then closes
// it exactly once for every caller.
type lifecycle struct {
	closeOnce func() error
	mu        sync.Mutex
	tasks     sync.WaitGroup
	closed    bool
}

func newLifecycle(next realtime.Closer) *lifecycle {
	life := &lifecycle{}
	life.closeOnce = sync.OnceValue(func() error { return life.release(next) })

	return life
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

func (l *lifecycle) close() error { return l.closeOnce() }

func (l *lifecycle) release(next realtime.Closer) error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()

	// Close before drain, inverted on purpose: closing next is what unblocks
	// tasks parked inside the provider. next.Close must be safe against, and
	// must unblock, in-flight calls.
	closeErr := next.Close()
	l.tasks.Wait()

	return closeErr
}

// Translator schedules machine-translation calls on a shared worker pool.
type Translator struct {
	next realtime.Translator
	pool *dispatch.Pool
	life *lifecycle
}

// NewTranslator bounds next with pool.
func NewTranslator(pool *dispatch.Pool, next realtime.Translator) *Translator {
	return &Translator{pool: pool, next: next, life: newLifecycle(next)}
}

// Supports delegates the capability query without consuming a worker slot.
func (t *Translator) Supports(from, to core.Lang) bool { return t.next.Supports(from, to) }

type translationResult struct {
	err  error
	text string
}

// Translate implements realtime.Translator.
func (t *Translator) Translate(
	ctx context.Context, source realtime.Segment, to core.Lang,
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
func (t *Translator) Close() error { return t.life.close() }

// Synthesizer schedules whole synthesis turns on a shared worker pool: a worker
// stays assigned while the stream drains, so the limit bounds inference itself.
type Synthesizer struct {
	next realtime.Synthesizer
	pool *dispatch.Pool
	life *lifecycle
}

// NewSynthesizer bounds next with pool.
func NewSynthesizer(pool *dispatch.Pool, next realtime.Synthesizer) *Synthesizer {
	return &Synthesizer{pool: pool, next: next, life: newLifecycle(next)}
}

type synthesisStart struct {
	err error
}

// Speak implements realtime.Synthesizer.
func (s *Synthesizer) Speak(
	ctx context.Context, to core.Lang, voice core.Voice, text <-chan string,
) (*realtime.AudioStream, error) {
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

		return realtime.NewAudioStream(output, result), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close releases the wrapped lane-scoped provider and waits for accepted tasks.
func (s *Synthesizer) Close() error { return s.life.close() }

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
	_ realtime.Translator  = (*Translator)(nil)
	_ realtime.Synthesizer = (*Synthesizer)(nil)
	_ realtime.Closer      = (*Translator)(nil)
	_ realtime.Closer      = (*Synthesizer)(nil)
)

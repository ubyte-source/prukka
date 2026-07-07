package bounded_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/providers/bounded"
)

type closingTranslator struct {
	started   chan struct{}
	release   chan struct{}
	finish    chan struct{}
	closeOnce sync.Once
}

func (*closingTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (t *closingTranslator) Translate(
	context.Context, engine.Segment, core.Lang,
) (string, error) {
	close(t.started)
	<-t.release
	<-t.finish

	return "done", nil
}

func (t *closingTranslator) Close() error {
	t.closeOnce.Do(func() { close(t.release) })

	return nil
}

type closingSynthesizer struct {
	started   chan struct{}
	release   chan struct{}
	finish    chan struct{}
	closeOnce sync.Once
}

func (s *closingSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	audio := make(chan core.PCM)
	result := make(chan error, 1)
	go func() {
		close(s.started)
		<-s.release
		<-s.finish
		result <- nil
		close(result)
		close(audio)
	}()

	return engine.NewAudioStream(audio, result), nil
}

func (s *closingSynthesizer) Close() error {
	s.closeOnce.Do(func() { close(s.release) })

	return nil
}

type observingTranslator struct {
	release <-chan struct{}
	live    atomic.Int64
	peak    atomic.Int64
}

func (*observingTranslator) Close() error { return nil }

func (*observingTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (t *observingTranslator) Translate(
	ctx context.Context, source engine.Segment, _ core.Lang,
) (string, error) {
	live := t.live.Add(1)
	defer t.live.Add(-1)

	for {
		peak := t.peak.Load()
		if live <= peak || t.peak.CompareAndSwap(peak, live) {
			break
		}
	}

	select {
	case <-t.release:
		return source.Text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestTranslatorHonorsSharedWorkerLimit(t *testing.T) {
	t.Parallel()

	const calls = 8

	release := make(chan struct{})
	next := &observingTranslator{release: release}
	pool := dispatch.New(2, calls)
	wrapped := bounded.NewTranslator(pool, next)

	done := make(chan error, calls)
	for range calls {
		go func() {
			_, err := wrapped.Translate(t.Context(), engine.Segment{Text: "ciao"}, "en")
			done <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for next.peak.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)

	for range calls {
		if err := <-done; err != nil {
			t.Fatalf("Translate returned %v", err)
		}
	}
	pool.Close()

	if got := next.peak.Load(); got != 2 {
		t.Fatalf("peak translation concurrency = %d, want 2", got)
	}
}

func TestTranslatorCloseWaitsForAcceptedTask(t *testing.T) {
	t.Parallel()

	next := &closingTranslator{
		started: make(chan struct{}), release: make(chan struct{}), finish: make(chan struct{}),
	}
	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewTranslator(pool, next)

	translated := make(chan error, 1)
	go func() {
		_, err := wrapped.Translate(context.Background(), engine.Segment{Text: "ciao"}, "en")
		translated <- err
	}()
	<-next.started

	closed := make(chan error, 1)
	go func() { closed <- wrapped.Close() }()
	<-next.release
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the accepted task: %v", err)
	default:
	}

	close(next.finish)
	if err := <-translated; err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := wrapped.Translate(t.Context(), engine.Segment{}, "en"); !errors.Is(err, bounded.ErrClosed) {
		t.Fatalf("Translate after Close = %v, want ErrClosed", err)
	}
}

type streamingSynthesizer struct {
	release <-chan struct{}
	started chan<- struct{}
}

func (streamingSynthesizer) Close() error { return nil }

func (s streamingSynthesizer) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, _ <-chan string,
) (*engine.AudioStream, error) {
	out := make(chan core.PCM)
	result := make(chan error, 1)
	go func() {
		s.started <- struct{}{}

		select {
		case <-s.release:
			result <- nil
		case <-ctx.Done():
			result <- ctx.Err()
		}
		close(result)
		close(out)
	}()

	return engine.NewAudioStream(out, result), nil
}

func TestSynthesizerHoldsWorkerUntilStreamEnds(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	starts := make(chan struct{}, 2)
	pool := dispatch.New(1, 2)
	wrapped := bounded.NewSynthesizer(pool, streamingSynthesizer{release: release, started: starts})

	first, err := wrapped.Speak(t.Context(), "en", core.Voice{ID: "voice"}, nil)
	if err != nil {
		t.Fatalf("first Speak: %v", err)
	}
	<-starts

	secondReady := make(chan error, 1)
	go func() {
		second, speakErr := wrapped.Speak(t.Context(), "en", core.Voice{ID: "voice"}, nil)
		if speakErr == nil {
			for frame := range second.Audio() {
				_ = frame
			}
			speakErr = second.Err()
		}
		secondReady <- speakErr
	}()

	select {
	case <-starts:
		t.Fatal("second synthesis started before the first stream ended")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	for frame := range first.Audio() {
		_ = frame
	}
	if err := first.Err(); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	if err := <-secondReady; err != nil {
		t.Fatalf("second Speak: %v", err)
	}
	pool.Close()
}

func TestSynthesizerCloseWaitsForAcceptedStream(t *testing.T) {
	t.Parallel()

	next := &closingSynthesizer{
		started: make(chan struct{}), release: make(chan struct{}), finish: make(chan struct{}),
	}
	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewSynthesizer(pool, next)

	audio, err := wrapped.Speak(context.Background(), "en", core.Voice{}, nil)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	<-next.started

	closed := make(chan error, 1)
	go func() { closed <- wrapped.Close() }()
	<-next.release
	select {
	case closeErr := <-closed:
		t.Fatalf("Close returned before the accepted stream: %v", closeErr)
	default:
	}

	close(next.finish)
	for frame := range audio.Audio() {
		_ = frame
	}
	if streamErr := audio.Err(); streamErr != nil {
		t.Fatalf("stream: %v", streamErr)
	}
	if closeErr := <-closed; closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if _, speakErr := wrapped.Speak(t.Context(), "en", core.Voice{}, nil); !errors.Is(speakErr, bounded.ErrClosed) {
		t.Fatalf("Speak after Close = %v, want ErrClosed", speakErr)
	}
}

type failingSynthesizer struct{ err error }

func (failingSynthesizer) Close() error { return nil }

func (s failingSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	return nil, s.err
}

type nilStreamSynthesizer struct{ nilAudio bool }

func (nilStreamSynthesizer) Close() error { return nil }

func (s nilStreamSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	if !s.nilAudio {
		return nil, errors.Join()
	}
	result := make(chan error, 1)
	result <- nil
	close(result)

	return engine.NewAudioStream(nil, result), nil
}

type terminalSynthesizer struct{ err error }

func (terminalSynthesizer) Close() error { return nil }

func (s terminalSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	audio := make(chan core.PCM)
	close(audio)
	result := make(chan error, 1)
	result <- s.err
	close(result)

	return engine.NewAudioStream(audio, result), nil
}

func TestSynthesizerForwardsTerminalFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("helper exited after start")
	pool := dispatch.New(1, 1)
	wrapped := bounded.NewSynthesizer(pool, terminalSynthesizer{err: want})

	audio, err := wrapped.Speak(t.Context(), "en", core.Voice{}, nil)
	if err != nil {
		t.Fatalf("Speak returned start error: %v", err)
	}
	for frame := range audio.Audio() {
		_ = frame
	}
	if err := audio.Err(); !errors.Is(err, want) {
		t.Fatalf("stream error = %v, want %v", err, want)
	}
	pool.Close()
}

func TestSynthesizerReturnsProviderStartFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("model unavailable")
	pool := dispatch.New(1, 1)
	wrapped := bounded.NewSynthesizer(pool, failingSynthesizer{err: want})

	audio, err := wrapped.Speak(t.Context(), "en", core.Voice{}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("Speak error = %v, want %v", err, want)
	}
	if audio != nil {
		t.Fatal("Speak returned audio after a provider start failure")
	}
	pool.Close()
}

func TestSynthesizerRejectsNilProviderStream(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		nilAudio bool
	}{
		{name: "nil stream"},
		{name: "nil audio channel", nilAudio: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pool := dispatch.New(1, 1)
			wrapped := bounded.NewSynthesizer(pool, nilStreamSynthesizer{nilAudio: test.nilAudio})
			audio, err := wrapped.Speak(t.Context(), "en", core.Voice{}, nil)
			if err == nil || !strings.Contains(err.Error(), "nil audio stream") {
				t.Fatalf("Speak error = %v, want nil stream failure", err)
			}
			if audio != nil {
				t.Fatal("Speak returned audio for an invalid provider stream")
			}
			if closeErr := wrapped.Close(); closeErr != nil {
				t.Fatalf("Close: %v", closeErr)
			}
			pool.Close()
		})
	}
}

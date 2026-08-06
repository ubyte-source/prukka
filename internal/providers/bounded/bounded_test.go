package bounded_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
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
	context.Context, realtime.Segment, core.Lang,
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
) (*realtime.AudioStream, error) {
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

	return realtime.NewAudioStream(audio, result), nil
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
	ctx context.Context, source realtime.Segment, _ core.Lang,
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

// TestTranslatorHonorsSharedWorkerLimit runs in a bubble, so synctest.Wait
// answers "every worker that can start has started" exactly.
func TestTranslatorHonorsSharedWorkerLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const calls = 8

		release := make(chan struct{})
		next := &observingTranslator{release: release}
		pool := dispatch.New(2, calls)
		wrapped := bounded.NewTranslator(pool, next)

		done := make(chan error, calls)
		for range calls {
			go func() {
				_, err := wrapped.Translate(t.Context(), realtime.Segment{Text: "ciao"}, "en")
				done <- err
			}()
		}

		// Every worker is now parked on release, so the peak is final.
		synctest.Wait()
		if got := next.peak.Load(); got != 2 {
			t.Fatalf("peak translation concurrency = %d, want exactly the 2 workers", got)
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
	})
}

func TestTranslatorCloseWaitsForAcceptedTask(t *testing.T) {
	t.Parallel()

	next := &closingTranslator{
		started: make(chan struct{}),
		release: make(chan struct{}),
		finish:  make(chan struct{}),
	}
	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewTranslator(pool, next)

	translated := make(chan error, 1)
	go func() {
		_, err := wrapped.Translate(context.Background(), realtime.Segment{Text: "ciao"}, "en")
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
	if _, err := wrapped.Translate(t.Context(), realtime.Segment{}, "en"); !errors.Is(err, bounded.ErrClosed) {
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
) (*realtime.AudioStream, error) {
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

	return realtime.NewAudioStream(out, result), nil
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
		started: make(chan struct{}),
		release: make(chan struct{}),
		finish:  make(chan struct{}),
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
) (*realtime.AudioStream, error) {
	return nil, s.err
}

type nilStreamSynthesizer struct{ nilAudio bool }

func (nilStreamSynthesizer) Close() error { return nil }

func (s nilStreamSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*realtime.AudioStream, error) {
	if !s.nilAudio {
		// No stream AND no error: the shape the wrapper must turn into a failure.
		var missing *realtime.AudioStream

		return missing, nil
	}
	result := make(chan error, 1)
	result <- nil
	close(result)

	return realtime.NewAudioStream(nil, result), nil
}

type terminalSynthesizer struct{ err error }

func (terminalSynthesizer) Close() error { return nil }

func (s terminalSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*realtime.AudioStream, error) {
	audio := make(chan core.PCM)
	close(audio)
	result := make(chan error, 1)
	result <- s.err
	close(result)

	return realtime.NewAudioStream(audio, result), nil
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

// echoTranslator answers with a marked transform, so the pass-through shows.
type echoTranslator struct{}

func (echoTranslator) Close() error { return nil }

func (echoTranslator) Supports(from, _ core.Lang) bool { return from == "it" }

func (echoTranslator) Translate(
	_ context.Context, source realtime.Segment, to core.Lang,
) (string, error) {
	return "mt:" + source.Text + ">" + string(to), nil
}

func TestTranslatorReturnsInnerTranslationVerbatim(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewTranslator(pool, echoTranslator{})

	got, err := wrapped.Translate(t.Context(), realtime.Segment{Text: "ciao", Lang: "it"}, "en")
	if err != nil || got != "mt:ciao>en" {
		t.Fatalf("Translate = (%q, %v), want the inner translation verbatim", got, err)
	}
}

// Supports must answer while the pool's only worker is parked inside Translate.
func TestTranslatorSupportsAnswersWhileWorkerBusy(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		next := &observingTranslator{release: release}
		pool := dispatch.New(1, 1)
		defer pool.Close()
		wrapped := bounded.NewTranslator(pool, next)

		done := make(chan error, 1)
		go func() {
			_, err := wrapped.Translate(t.Context(), realtime.Segment{Text: "occupa"}, "en")
			done <- err
		}()
		synctest.Wait()
		if next.live.Load() != 1 {
			t.Fatal("the occupying Translate never reached the inner provider")
		}

		answered := make(chan bool, 1)
		go func() { answered <- wrapped.Supports("it", "en") }()

		// The bubble settles, so a Supports still waiting has simply not answered.
		synctest.Wait()
		select {
		case ok := <-answered:
			if !ok {
				t.Fatal("Supports = false, want the inner capability answer")
			}
		default:
			t.Fatal("Supports blocked; it must not consume a worker slot")
		}

		close(release)
		if err := <-done; err != nil {
			t.Fatalf("occupying Translate: %v", err)
		}
	})
}

// chunkSynthesizer streams a fixed chunk sequence and then a clean end.
type chunkSynthesizer struct{ chunks []core.PCM }

func (chunkSynthesizer) Close() error { return nil }

func (s chunkSynthesizer) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*realtime.AudioStream, error) {
	audio := make(chan core.PCM, len(s.chunks))
	for _, chunk := range s.chunks {
		audio <- chunk
	}
	close(audio)
	result := make(chan error, 1)
	result <- nil
	close(result)

	return realtime.NewAudioStream(audio, result), nil
}

// The wrapper's whole data-path contract: every chunk the provider emits
// arrives on the wrapper's channel, in order and unmodified.
func TestSynthesizerDeliversChunksInOrderUnmodified(t *testing.T) {
	t.Parallel()

	chunks := []core.PCM{
		{Data: []int16{1, 2}, Rate: 16000, Ch: 1},
		{Data: []int16{3}, Rate: 16000, Ch: 1, PTS: 20 * time.Millisecond},
		{Data: []int16{4, 5, 6}, Rate: 16000, Ch: 1, PTS: 40 * time.Millisecond},
	}
	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewSynthesizer(pool, chunkSynthesizer{chunks: chunks})

	audio, err := wrapped.Speak(t.Context(), "en", core.Voice{}, nil)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	var got []core.PCM
	for chunk := range audio.Audio() {
		got = append(got, chunk)
	}
	if streamErr := audio.Err(); streamErr != nil {
		t.Fatalf("stream end: %v", streamErr)
	}
	if !reflect.DeepEqual(got, chunks) {
		t.Fatalf("delivered chunks = %+v, want %+v in order", got, chunks)
	}
}

// cancelableStreamSynthesizer emits chunks until the call context ends, then
// posts the terminal result.
type cancelableStreamSynthesizer struct{ started chan<- struct{} }

func (cancelableStreamSynthesizer) Close() error { return nil }

func (s cancelableStreamSynthesizer) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, _ <-chan string,
) (*realtime.AudioStream, error) {
	audio := make(chan core.PCM)
	result := make(chan error, 1)
	go func() {
		s.started <- struct{}{}
		for {
			select {
			case audio <- core.PCM{Data: []int16{1}, Rate: 16000, Ch: 1}:
			case <-ctx.Done():
				result <- ctx.Err()
				close(result)
				close(audio)

				return
			}
		}
	}()

	return realtime.NewAudioStream(audio, result), nil
}

// An abandoned, undrained stream must still release its pool worker on
// cancellation, or every oversized take leaks one of the few workers.
func TestSynthesizerAbandonedStreamReleasesWorkerOnCancel(t *testing.T) {
	t.Parallel()

	starts := make(chan struct{}, 2)
	pool := dispatch.New(1, 2)
	defer pool.Close()
	wrapped := bounded.NewSynthesizer(pool, cancelableStreamSynthesizer{started: starts})

	ctx, cancel := context.WithCancel(t.Context())
	first, err := wrapped.Speak(ctx, "en", core.Voice{ID: "voice"}, nil)
	if err != nil {
		t.Fatalf("first Speak: %v", err)
	}
	if first == nil {
		t.Fatal("first Speak returned no stream")
	}
	<-starts
	// Never drained: cancel while forward is parked on the blocked send, the
	// only exit that frees the worker.
	cancel()

	secondCtx, secondCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer secondCancel()
	second, err := wrapped.Speak(secondCtx, "en", core.Voice{ID: "voice"}, nil)
	if err != nil {
		t.Fatalf("second Speak after abandonment = %v, want a released worker", err)
	}
	<-starts

	secondCancel()
	for frame := range second.Audio() {
		_ = frame
	}
	if streamErr := second.Err(); !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("second stream end = %v, want its own cancellation", streamErr)
	}
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

// countingTranslator records how often the wrapped provider was released.
type countingTranslator struct {
	err    error
	closes atomic.Int64
}

func (*countingTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (*countingTranslator) Translate(context.Context, realtime.Segment, core.Lang) (string, error) {
	return "", nil
}

func (t *countingTranslator) Close() error {
	t.closes.Add(1)

	return t.err
}

// TestCloseReleasesTheProviderOnceAndCachesItsFailure: a lane teardown and a
// daemon shutdown can both close one bounded provider.
func TestCloseReleasesTheProviderOnceAndCachesItsFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("provider release failed")
	next := &countingTranslator{err: want}
	pool := dispatch.New(1, 1)
	defer pool.Close()
	wrapped := bounded.NewTranslator(pool, next)

	const closers = 8
	results := make(chan error, closers)
	start := make(chan struct{})
	for range closers {
		go func() {
			<-start
			results <- wrapped.Close()
		}()
	}
	close(start)
	for range closers {
		if err := <-results; !errors.Is(err, want) {
			t.Fatalf("concurrent Close = %v, want the cached %v", err, want)
		}
	}
	if got := next.closes.Load(); got != 1 {
		t.Fatalf("wrapped provider released %d times, want exactly once", got)
	}
}

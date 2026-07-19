package engine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/pipeline"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// segmentingTranscriber emits one committed segment per pushed frame, so the
// engine drives one full translate/caption/synthesize cycle per frame.
type segmentingTranscriber struct{}

func (segmentingTranscriber) Open(ctx context.Context, hint core.Lang) (engine.Transcription, error) {
	session := &segmentingTranscription{events: make(chan engine.Transcript, 16), hint: hint}

	go func() {
		<-ctx.Done()
		session.close()
	}()

	return session, nil
}

type segmentingTranscription struct {
	events chan engine.Transcript
	hint   core.Lang
	once   sync.Once
	n      int
}

func (f *segmentingTranscription) Push(core.PCM) error {
	f.n++
	f.events <- engine.Transcript{Text: fmt.Sprintf("seg%d", f.n), Lang: f.hint, Stable: true, Final: true}

	return nil
}

func (f *segmentingTranscription) Events() <-chan engine.Transcript { return f.events }

func (f *segmentingTranscription) Err() error { return nil }

func (f *segmentingTranscription) CloseSend() error {
	f.close()

	return nil
}

func (f *segmentingTranscription) Close() error {
	f.close()

	return nil
}

func (f *segmentingTranscription) close() { f.once.Do(func() { close(f.events) }) }

// prefixTranslator tags each segment with its target so the caption identifies
// both the source segment and the language it was routed to.
type prefixTranslator struct{}

func (prefixTranslator) Close() error { return nil }

func (prefixTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (prefixTranslator) Translate(
	_ context.Context, source engine.Segment, to core.Lang,
) (string, error) {
	return string(to) + ":" + source.Text, nil
}

type unsupportedTranslator struct{}

func (unsupportedTranslator) Close() error { return nil }

func (unsupportedTranslator) Supports(core.Lang, core.Lang) bool { return false }

func (unsupportedTranslator) Translate(context.Context, engine.Segment, core.Lang) (string, error) {
	return "", errors.New("Translate must not run without a declared model")
}

// recordingSynth records each clause it is asked to speak and answers with one
// PCM chunk of that clause's length, filled with ones so the track is legible.
type recordingSynth struct {
	clauses []string
	mu      sync.Mutex
}

func (*recordingSynth) Close() error { return nil }

func (s *recordingSynth) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, text <-chan string,
) (*engine.AudioStream, error) {
	out := make(chan core.PCM, 4)
	result := make(chan error, 1)

	go func() {
		for clause := range text {
			s.mu.Lock()
			s.clauses = append(s.clauses, clause)
			s.mu.Unlock()

			select {
			case out <- core.PCM{Data: ones(len(clause)), Rate: 16000, Ch: 1}:
			case <-ctx.Done():
				result <- ctx.Err()
				close(result)
				close(out)

				return
			}
		}
		result <- nil
		close(result)
		close(out)
	}()

	return engine.NewAudioStream(out, result), nil
}

func (s *recordingSynth) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.clauses...)
}

func ones(n int) []int16 {
	data := make([]int16, n)
	for i := range data {
		data[i] = 1
	}

	return data
}

// finiteFrames yields max frames then EOF, pacing the source clock forward.
type finiteFrames struct {
	n   int
	max int
}

func (f *finiteFrames) Next(context.Context) (core.PCM, error) {
	if f.n >= f.max {
		return core.PCM{}, io.EOF
	}

	f.n++

	return core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1, PTS: time.Duration(f.n) * 100 * time.Millisecond}, nil
}

// blockingFrames never yields; it waits for cancellation.
type blockingFrames struct{}

func (blockingFrames) Next(ctx context.Context) (core.PCM, error) {
	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

type failingFrames struct{ err error }

func (f failingFrames) Next(context.Context) (core.PCM, error) { return core.PCM{}, f.err }

type oneThenBlockingFrames struct{ sent bool }

func (f *oneThenBlockingFrames) Next(ctx context.Context) (core.PCM, error) {
	if !f.sent {
		f.sent = true

		return core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1, PTS: 100 * time.Millisecond}, nil
	}

	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

type targetFailureFrames struct {
	readStarted chan struct{}
	released    chan struct{}
	closeErr    error
	startOnce   sync.Once
	releaseOnce sync.Once
	closes      atomic.Int32
	sent        bool
}

func newTargetFailureFrames() *targetFailureFrames {
	return &targetFailureFrames{readStarted: make(chan struct{}), released: make(chan struct{})}
}

func (f *targetFailureFrames) Next(context.Context) (core.PCM, error) {
	if !f.sent {
		f.sent = true

		return core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1, PTS: 100 * time.Millisecond}, nil
	}

	f.startOnce.Do(func() { close(f.readStarted) })
	<-f.released

	return core.PCM{}, io.ErrClosedPipe
}

func (f *targetFailureFrames) Close() error {
	f.closes.Add(1)
	f.releaseOnce.Do(func() { close(f.released) })

	return f.closeErr
}

type failAfterSourceBlocksTranslator struct {
	blocked <-chan struct{}
	err     error
}

func (failAfterSourceBlocksTranslator) Close() error { return nil }

func (failAfterSourceBlocksTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (t failAfterSourceBlocksTranslator) Translate(
	ctx context.Context, _ engine.Segment, _ core.Lang,
) (string, error) {
	select {
	case <-t.blocked:
		return "", t.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type twoThenBlockingFrames struct{ sent int }

func (f *twoThenBlockingFrames) Next(ctx context.Context) (core.PCM, error) {
	if f.sent < 2 {
		f.sent++

		return core.PCM{
			Data: make([]int16, 320), Rate: 16000, Ch: 1,
			PTS: time.Duration(f.sent) * 100 * time.Millisecond,
		}, nil
	}

	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

type failingTranslator struct{ err error }

func (failingTranslator) Close() error { return nil }

func (f failingTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (f failingTranslator) Translate(context.Context, engine.Segment, core.Lang) (string, error) {
	return "", f.err
}

type cancelObservedTranslator struct {
	secondStarted chan struct{}
	secondExited  chan struct{}
	calls         int
	mu            sync.Mutex
}

func (*cancelObservedTranslator) Close() error { return nil }

func (*cancelObservedTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (t *cancelObservedTranslator) Translate(
	ctx context.Context, source engine.Segment, _ core.Lang,
) (string, error) {
	t.mu.Lock()
	t.calls++
	call := t.calls
	t.mu.Unlock()
	if call == 1 {
		return source.Text, nil
	}

	close(t.secondStarted)
	<-ctx.Done()
	close(t.secondExited)

	return "", ctx.Err()
}

type failAfterSignalSynth struct{ ready <-chan struct{} }

func (failAfterSignalSynth) Close() error { return nil }

func (s failAfterSignalSynth) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	<-s.ready
	audio := make(chan core.PCM)
	close(audio)
	result := make(chan error, 1)
	result <- errors.New("voice failed")
	close(result)

	return engine.NewAudioStream(audio, result), nil
}

type terminalTranscriber struct{ err error }

func (t terminalTranscriber) Open(context.Context, core.Lang) (engine.Transcription, error) {
	events := make(chan engine.Transcript)
	close(events)

	return terminalTranscription{events: events, err: t.err}, nil
}

type terminalTranscription struct {
	events <-chan engine.Transcript
	err    error
}

func (t terminalTranscription) Push(core.PCM) error              { return nil }
func (t terminalTranscription) Events() <-chan engine.Transcript { return t.events }
func (t terminalTranscription) Err() error                       { return t.err }
func (t terminalTranscription) CloseSend() error                 { return nil }
func (t terminalTranscription) Close() error                     { return nil }

type blockingCloseTranscriber struct {
	entered chan struct{}
	release chan struct{}
}

func (t blockingCloseTranscriber) Open(context.Context, core.Lang) (engine.Transcription, error) {
	return &blockingCloseTranscription{
		events: make(chan engine.Transcript), entered: t.entered, release: t.release,
	}, nil
}

type blockingCloseTranscription struct {
	events  chan engine.Transcript
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingCloseTranscription) Push(core.PCM) error { return nil }

func (t *blockingCloseTranscription) Events() <-chan engine.Transcript { return t.events }

func (*blockingCloseTranscription) Err() error { return nil }

func (*blockingCloseTranscription) CloseSend() error { return nil }

func (t *blockingCloseTranscription) Close() error {
	t.once.Do(func() {
		close(t.entered)
		<-t.release
		close(t.events)
	})

	return nil
}

type terminalSynth struct{ err error }

func (terminalSynth) Close() error { return nil }

func (s terminalSynth) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	audio := make(chan core.PCM)
	close(audio)
	result := make(chan error, 1)
	result <- s.err
	close(result)

	return engine.NewAudioStream(audio, result), nil
}

type recordingSink struct {
	segs []*core.TranslatedSegment
	mu   sync.Mutex
}

func (s *recordingSink) Append(seg *core.TranslatedSegment) {
	s.mu.Lock()
	s.segs = append(s.segs, seg)
	s.mu.Unlock()
}

func (s *recordingSink) captions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.segs))
	for i, seg := range s.segs {
		out[i] = seg.Text
	}

	return out
}

func TestEngineCaptionsAndDubs(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	synth := &recordingSynth{}
	track := pipeline.NewTrack()

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: segmentingTranscriber{}, Translator: prefixTranslator{}},
		Output: engine.Output{
			Sinks: map[core.Lang]engine.Sink{"en": sink},
			Dub: &engine.Dub{
				Synthesizer: synth,
				Tracks:      map[core.Lang]engine.VoiceSink{"en": track},
				Voices:      map[core.Lang]core.Voice{"en": {ID: "v"}},
			},
		},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 3}); err != nil {
		t.Fatalf("run: %v", err)
	}

	wantCaptions := []string{"en:seg1", "en:seg2", "en:seg3"}
	if got := sink.captions(); !equal(got, wantCaptions) {
		t.Fatalf("captions = %v, want %v", got, wantCaptions)
	}

	if got := synth.recorded(); !equal(got, wantCaptions) {
		t.Fatalf("synthesized = %v, want the caption texts %v", got, wantCaptions)
	}

	if _, ok := track.Start(); !ok {
		t.Fatal("track received no dubbed audio")
	}
}

func TestEngineCaptionsOnly(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: segmentingTranscriber{}, Translator: prefixTranslator{}},
		Output:    engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 2}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := sink.captions(); !equal(got, []string{"en:seg1", "en:seg2"}) {
		t.Fatalf("captions = %v, want two segments", got)
	}
}

func TestEngineContextCancelStops(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: segmentingTranscriber{}, Translator: prefixTranslator{}},
		Output:    engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := engine.New(cfg, discardLog()).Run(ctx, blockingFrames{}); err == nil {
		t.Fatal("run under a canceled context must return an error")
	}
}

func TestEngineCancellationWaitsForTranscriptionClose(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "cleanup", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: blockingCloseTranscriber{entered: entered, release: release},
			Translator:  prefixTranslator{},
		},
		Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"it": &recordingSink{}}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.New(cfg, discardLog()).Run(ctx, blockingFrames{}) }()
	cancel()

	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatalf("Close was not called: %v", t.Context().Err())
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned before transcription cleanup: %v", err)
	default:
	}

	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
}

func TestEngineStageFailureCancelsTranscription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		translator engine.Translator
		frames     core.Frames
		want       string
	}{
		{
			name:       "source",
			translator: prefixTranslator{},
			frames:     failingFrames{err: errors.New("capture failed")},
			want:       "source: capture failed",
		},
		{
			name:       "translator",
			translator: failingTranslator{err: errors.New("model failed")},
			frames:     &oneThenBlockingFrames{},
			want:       "translate it to en: model failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &engine.Config{
				Stream: engine.Stream{Session: "cancel-on-error", Track: "main", Source: "it"},
				Providers: engine.Providers{
					Transcriber: segmentingTranscriber{}, Translator: tt.translator,
				},
				Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}}},
			}
			done := make(chan error, 1)
			go func() { done <- engine.New(cfg, discardLog()).Run(context.Background(), tt.frames) }()

			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("Run error = %v, want %q", err, tt.want)
				}
			case <-time.After(time.Second):
				t.Fatal("Run deadlocked after a pipeline stage failed")
			}
		})
	}
}

func TestTargetFailureClosesBlockedSource(t *testing.T) {
	frames := newTargetFailureFrames()
	want := errors.New("model failed after source blocked")
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "close-source", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: segmentingTranscriber{},
			Translator: failAfterSourceBlocksTranslator{
				blocked: frames.readStarted,
				err:     want,
			},
		},
		Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engine.New(cfg, discardLog()).Run(ctx, frames) }()

	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("Run error = %v, want target failure", err)
		}
	case <-time.After(time.Second):
		cancel()
		if err := frames.Close(); err != nil {
			t.Errorf("timeout cleanup Close error = %v", err)
		}
		t.Fatal("Run did not close the source blocked after a target failure")
	}

	if got := frames.closes.Load(); got != 1 {
		t.Fatalf("source Close calls = %d, want 1", got)
	}
}

func TestSynthesisFailureWaitsForTranslationStage(t *testing.T) {
	t.Parallel()

	translator := &cancelObservedTranslator{
		secondStarted: make(chan struct{}), secondExited: make(chan struct{}),
	}
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "join-target", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: segmentingTranscriber{}, Translator: translator,
		},
		Output: engine.Output{
			Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}},
			Dub: &engine.Dub{
				Synthesizer: failAfterSignalSynth{ready: translator.secondStarted},
				Tracks:      map[core.Lang]engine.VoiceSink{"en": pipeline.NewTrack()},
				Voices:      map[core.Lang]core.Voice{"en": {ID: "v", Lang: "en"}},
			},
		},
	}

	err := engine.New(cfg, discardLog()).Run(t.Context(), &twoThenBlockingFrames{})
	if err == nil || !strings.Contains(err.Error(), "voice failed") {
		t.Fatalf("Run error = %v, want synthesis failure", err)
	}
	select {
	case <-translator.secondExited:
	default:
		t.Fatal("Run returned before the translation stage exited")
	}
}

func TestEnginePropagatesTerminalProviderErrors(t *testing.T) {
	t.Parallel()

	t.Run("transcription", func(t *testing.T) {
		t.Parallel()

		cfg := &engine.Config{
			Stream: engine.Stream{Session: "terminal-stt", Track: "main", Source: "it"},
			Providers: engine.Providers{
				Transcriber: terminalTranscriber{err: errors.New("helper exited")},
				Translator:  prefixTranslator{},
			},
			Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}}},
		}
		err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 1})
		if err == nil || !strings.Contains(err.Error(), "transcription: helper exited") {
			t.Fatalf("Run error = %v, want terminal transcription failure", err)
		}
	})

	t.Run("synthesis", func(t *testing.T) {
		t.Parallel()

		cfg := &engine.Config{
			Stream: engine.Stream{Session: "terminal-tts", Track: "main", Source: "it"},
			Providers: engine.Providers{
				Transcriber: segmentingTranscriber{}, Translator: prefixTranslator{},
			},
			Output: engine.Output{
				Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}},
				Dub: &engine.Dub{
					Synthesizer: terminalSynth{err: errors.New("voice helper exited")},
					Tracks:      map[core.Lang]engine.VoiceSink{"en": pipeline.NewTrack()},
					Voices:      map[core.Lang]core.Voice{"en": {ID: "v", Lang: "en"}},
				},
			},
		}
		err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 1})
		if err == nil || !strings.Contains(err.Error(), "synthesize en: voice helper exited") {
			t.Fatalf("Run error = %v, want terminal synthesis failure", err)
		}
	})
}

func TestEngineRejectsUnsupportedTranslationPair(t *testing.T) {
	t.Parallel()

	for _, source := range []core.Lang{"it", core.LangAuto} {
		var transcriber engine.Transcriber = segmentingTranscriber{}
		if source == core.LangAuto {
			transcriber = scriptedTranscriber{script: []engine.Transcript{
				{Text: "seg1", Lang: "it", Stable: true, Final: true},
			}}
		}
		cfg := &engine.Config{
			Stream: engine.Stream{Session: "s", Track: "main", Source: source},
			Providers: engine.Providers{
				Transcriber: transcriber, Translator: unsupportedTranslator{},
			},
			Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}}},
		}

		err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 1})
		if err == nil || !strings.Contains(err.Error(), "translation model unavailable for it to en") {
			t.Fatalf("source %q: error = %v, want unavailable pair", source, err)
		}
	}
}

// scriptedTranscriber replays a fixed sequence of transcript updates, one per
// pushed frame, so a test can drive the engine's wait-k commit policy with
// revising partials rather than one committed segment per frame.
type scriptedTranscriber struct{ script []engine.Transcript }

func (s scriptedTranscriber) Open(ctx context.Context, _ core.Lang) (engine.Transcription, error) {
	session := &scriptedTranscription{events: make(chan engine.Transcript, 16), script: s.script}

	go func() {
		<-ctx.Done()
		session.close()
	}()

	return session, nil
}

type scriptedTranscription struct {
	events chan engine.Transcript
	script []engine.Transcript
	once   sync.Once
	n      int
}

func (s *scriptedTranscription) Push(core.PCM) error {
	if s.n < len(s.script) {
		s.events <- s.script[s.n]
		s.n++
	}

	return nil
}

func (s *scriptedTranscription) Events() <-chan engine.Transcript { return s.events }

func (s *scriptedTranscription) Err() error { return nil }

func (s *scriptedTranscription) CloseSend() error {
	s.close()

	return nil
}

func (s *scriptedTranscription) Close() error {
	s.close()

	return nil
}

func (s *scriptedTranscription) close() { s.once.Do(func() { close(s.events) }) }

func TestEngineCommitsClausesFromPartials(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	ten := "uno due tre quattro cinque sei sette otto nove dieci"
	script := []engine.Transcript{
		{Text: ten, Lang: "it"},
		{Text: ten, Lang: "it"},
		{Text: ten, Lang: "it", Stable: true, Final: true},
	}

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: scriptedTranscriber{script: script}, Translator: prefixTranslator{}},
		Output:    engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 3}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Two identical partials let local agreement commit the first maxClause
	// words before the final flushes the withheld tail.
	want := []string{"en:uno due tre quattro cinque sei sette otto", "en:nove dieci"}
	if got := sink.captions(); !equal(got, want) {
		t.Fatalf("captions = %v, want %v", got, want)
	}
}

func TestEngineEmptyFinalAdvancesTheNextTurnBoundary(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	script := []engine.Transcript{
		{Lang: "it", Stable: true, Final: true},
		{Text: "secondo turno", Lang: "it", Stable: true, Final: true},
	}
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "empty-final", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: scriptedTranscriber{script: script}, Translator: prefixTranslator{},
		},
		Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 2}); err != nil {
		t.Fatalf("run: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.segs) != 1 {
		t.Fatalf("segments = %d, want one recognized turn", len(sink.segs))
	}
	if start := sink.segs[0].ScheduleAt; start <= 0 {
		t.Fatalf("second turn starts at %v, want the empty-final boundary", start)
	}
}

func TestEngineUsesTranscriptSourceBoundariesInsteadOfReceiptClock(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	script := []engine.Transcript{
		{},
		{Lang: "it", SourceEnd: 50 * time.Millisecond, Stable: true, Final: true, HasSourceEnd: true},
		{
			Text: "secondo turno", Lang: "it", SourceEnd: 90 * time.Millisecond,
			Stable: true, Final: true, HasSourceEnd: true,
		},
	}
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "source-boundaries", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: scriptedTranscriber{script: script}, Translator: prefixTranslator{},
		},
		Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 3}); err != nil {
		t.Fatalf("run: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.segs) != 1 {
		t.Fatalf("segments = %d, want one", len(sink.segs))
	}
	if got := sink.segs[0].ScheduleAt; got != 50*time.Millisecond {
		t.Fatalf("ScheduleAt = %v, want endpoint boundary 50ms", got)
	}
	if got := sink.segs[0].Duration; got != 40*time.Millisecond {
		t.Fatalf("Duration = %v, want endpoint span 40ms", got)
	}
}

func TestEngineRejectsNegativeTranscriptSourceBoundary(t *testing.T) {
	t.Parallel()

	script := []engine.Transcript{{
		Text: "invalid", Lang: "it", SourceEnd: -time.Millisecond,
		Stable: true, Final: true, HasSourceEnd: true,
	}}
	cfg := &engine.Config{
		Stream: engine.Stream{Session: "negative-boundary", Track: "main", Source: "it"},
		Providers: engine.Providers{
			Transcriber: scriptedTranscriber{script: script}, Translator: prefixTranslator{},
		},
		Output: engine.Output{Sinks: map[core.Lang]engine.Sink{"en": &recordingSink{}}},
	}

	err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 1})
	if err == nil || !strings.Contains(err.Error(), "source time moved backward") {
		t.Fatalf("run error = %v, want invalid source-time error", err)
	}
}

// gatedSynth blocks the first take until its gate is released, so a test can
// prove captions for later takes still ship while synthesis is stalled.
type gatedSynth struct {
	gate    chan struct{}
	clauses []string
	mu      sync.Mutex
	n       int
}

func (*gatedSynth) Close() error { return nil }

func (s *gatedSynth) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, text <-chan string,
) (*engine.AudioStream, error) {
	out := make(chan core.PCM, 4)
	result := make(chan error, 1)

	go func() {
		for clause := range text {
			s.mu.Lock()
			s.n++
			first := s.n == 1
			s.clauses = append(s.clauses, clause)
			s.mu.Unlock()

			if first {
				select {
				case <-s.gate:
				case <-ctx.Done():
					result <- ctx.Err()
					close(result)
					close(out)

					return
				}
			}

			select {
			case out <- core.PCM{Data: ones(len(clause)), Rate: 16000, Ch: 1}:
			case <-ctx.Done():
				result <- ctx.Err()
				close(result)
				close(out)

				return
			}
		}
		result <- nil
		close(result)
		close(out)
	}()

	return engine.NewAudioStream(out, result), nil
}

func TestEngineCaptionsLeadSlowSynthesis(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	synth := &gatedSynth{gate: make(chan struct{})}

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: segmentingTranscriber{}, Translator: prefixTranslator{}},
		Output: engine.Output{
			Sinks: map[core.Lang]engine.Sink{"en": sink},
			Dub: &engine.Dub{
				Synthesizer: synth,
				Tracks:      map[core.Lang]engine.VoiceSink{"en": pipeline.NewTrack()},
				Voices:      map[core.Lang]core.Voice{"en": {ID: "v"}},
			},
		},
	}

	done := make(chan error, 1)
	go func() { done <- engine.New(cfg, discardLog()).Run(context.Background(), &finiteFrames{max: 3}) }()

	// All three captions must land even though the first take's synthesis is
	// still blocked on the gate — the translate stage runs ahead of the voice.
	waitFor(t, func() bool { return len(sink.captions()) == 3 })

	close(synth.gate)

	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	testkit.Eventually(t, 2*time.Second, cond, "condition not met before deadline")
}

func TestEngineFlushesHeldTailAtStreamEnd(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	ten := "uno due tre quattro cinque sei sette otto nove dieci"
	// Two identical partials, then the stream closes with NO Final — an audio
	// EOF mid-utterance. The committed prefix ships during the partials; the
	// held tail must still be released at stream end, not dropped.
	script := []engine.Transcript{{Text: ten, Lang: "it"}, {Text: ten, Lang: "it"}}

	cfg := &engine.Config{
		Stream:    engine.Stream{Session: "s", Track: "main", Source: "it"},
		Providers: engine.Providers{Transcriber: scriptedTranscriber{script: script}, Translator: prefixTranslator{}},
		Output:    engine.Output{Sinks: map[core.Lang]engine.Sink{"en": sink}},
	}

	if err := engine.New(cfg, discardLog()).Run(t.Context(), &finiteFrames{max: 2}); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []string{"en:uno due tre quattro cinque sei sette otto", "en:nove dieci"}
	if got := sink.captions(); !equal(got, want) {
		t.Fatalf("captions = %v, want %v (held tail must not be dropped)", got, want)
	}
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

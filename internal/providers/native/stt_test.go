package native

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// TestMain lets the test binary impersonate the spawned engine: run with a stub
// model or voice it replays a scripted stream across the real process boundary
// instead of executing the suite, so spawn/pipe/reap are exercised for real
// without a separate fixture executable.
func TestMain(m *testing.M) {
	if runFakeEngine(os.Args) {
		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestSTTStreamsTranscripts(t *testing.T) {
	t.Parallel()

	inferences := make(chan struct {
		kind string
		took time.Duration
	}, 2)
	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeModel, Rate: 16000,
		Inference: func(kind string, took time.Duration) {
			inferences <- struct {
				kind string
				took time.Duration
			}{kind: kind, took: took}
		},
	}).
		Open(t.Context(), core.Lang("it"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if pushErr := transcription.Push(core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1}); pushErr != nil {
		t.Fatalf("push: %v", pushErr)
	}

	if closeErr := transcription.CloseSend(); closeErr != nil {
		t.Fatalf("close send: %v", closeErr)
	}

	got := collect(transcription.Events())
	want := []engine.Transcript{
		{Text: "buon", Lang: "it", HasSourceEnd: true},
		{Text: "buongiorno", Lang: "it", HasSourceEnd: true},
		{Text: "Buongiorno a tutti.", Lang: "it", Stable: true, Final: true, HasSourceEnd: true},
		{Lang: "it", Stable: true, Final: true, HasSourceEnd: true},
		{Text: "il ponte", Lang: "it", HasSourceEnd: true},
		{Text: "Il ponte è aperto.", Lang: "it", Stable: true, Final: true, HasSourceEnd: true},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", got, want)
	}
	if terminalErr := transcription.Err(); terminalErr != nil {
		t.Fatalf("terminal error after clean stream = %v, want nil", terminalErr)
	}
	if got := <-inferences; got.kind != "partial" || got.took != 12500*time.Microsecond {
		t.Fatalf("first inference = %+v, want partial/12.5ms", got)
	}
	if got := <-inferences; got.kind != "final" || got.took != 20*time.Millisecond {
		t.Fatalf("second inference = %+v, want final/20ms", got)
	}
}

func TestSTTArgs(t *testing.T) {
	t.Parallel()

	stt := NewSTT(&STTConfig{
		Model: "m", Rate: 16000, Threads: 2,
		Tuning: STTTuning{
			SilenceHang: 160 * time.Millisecond, MaxWindow: 2 * time.Second,
			MinSpeech: 120 * time.Millisecond, PartialStride: 250 * time.Millisecond,
			FastDecode: true,
		},
	})

	got := stt.args(core.Lang("it-IT"))
	want := []string{
		"stt", "--protocol-version", "2", "--model", "m", "--rate", "16000", "--threads", "2",
		"--silence-hang", "160ms", "--max-window", "2s", "--min-speech", "120ms",
		"--partial-stride", "250ms", "--fast-decode", "--language", "it",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}

	gotAuto := stt.args(core.LangAuto)
	wantAuto := []string{
		"stt", "--protocol-version", "2", "--model", "m", "--rate", "16000", "--threads", "2",
		"--silence-hang", "160ms", "--max-window", "2s", "--min-speech", "120ms",
		"--partial-stride", "250ms", "--fast-decode",
	}
	if !reflect.DeepEqual(gotAuto, wantAuto) {
		t.Fatalf("auto args = %v, want %v", gotAuto, wantAuto)
	}

	defaultThreads := NewSTT(&STTConfig{Model: "m", Rate: 16000}).args(core.LangAuto)
	if got := flagValue(defaultThreads, flagThreads); got != "1" {
		t.Fatalf("default threads = %q, want 1", got)
	}
}

func TestSTTRejectsCanceledContextBeforeSpawn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := NewSTT(&STTConfig{Bin: os.Args[0], Model: fakeModel, Rate: 16000}).
		Open(ctx, core.Lang("it"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestSTTStartupExplainsProtocolVersionMismatch(t *testing.T) {
	t.Parallel()

	_, err := NewSTT(&STTConfig{Bin: os.Args[0], Model: fakeLegacySTT, Rate: 16000}).
		Open(t.Context(), core.Lang("it"))
	if err == nil || !strings.Contains(err.Error(), "protocol v2 startup failed") ||
		!strings.Contains(err.Error(), "rebuild an incompatible engine bundle") {
		t.Fatalf("Open error = %v, want actionable protocol-v2 rebuild guidance", err)
	}
}

func TestSTTRejectsUnexpectedPCMFormat(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	transcription, err := NewSTT(&STTConfig{Bin: os.Args[0], Model: fakeModel, Rate: 16000}).
		Open(ctx, core.Lang("it"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for _, frame := range []core.PCM{
		{Data: []int16{1}, Rate: 48000, Ch: 1},
		{Data: []int16{1, 2}, Rate: 16000, Ch: 2},
	} {
		if err := transcription.Push(frame); err == nil {
			t.Fatalf("Push(%+v) accepted an incompatible PCM format", frame)
		}
	}

	cancel()
	collect(transcription.Events())
}

func TestSTTRejectsInvalidDetectedLanguage(t *testing.T) {
	session := &sttSession{
		events: make(chan engine.Transcript, 1),
		stop:   make(chan struct{}),
		log:    slog.New(slog.DiscardHandler),
		lang:   "it",
	}
	session.readySeen.Store(true)

	err := session.dispatch([]byte(
		`{"partial":"ciao","language":"../../models/private","end_samples":0}`,
	))
	if err == nil || !strings.Contains(err.Error(), "response language") {
		t.Fatalf("dispatch error = %v, want invalid-language protocol error", err)
	}
}

func TestSTTRejectsMalformedProtocolShape(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		`{`,
		`{}`,
		`{"text":"ciao"}`,
		`{"partial":null}`,
		`{"text":null,"final":true}`,
		`{"partial":"ciao","text":"ciao","final":true}`,
		`{"partial":"ciao","final":true}`,
		`{"partial":"ciao","inference_ms":-1}`,
		`{"partial":"ciao","inference_ms":600001}`,
		`{"partial":"ciao","inference_ms":null}`,
		`{"partial":"ciao"}`,
		`{"partial":"ciao","end_samples":-1}`,
		`{"partial":"ciao","end_samples":1.5}`,
		`{"partial":"ciao","end_samples":null}`,
		`{"ready":false}`,
		`{"ready":true,"partial":"ciao"}`,
	} {
		session := &sttSession{
			events: make(chan engine.Transcript, 1), stop: make(chan struct{}),
			log: slog.New(slog.DiscardHandler), lang: "it",
		}
		session.readySeen.Store(true)
		if err := session.dispatch([]byte(line)); err == nil {
			t.Errorf("dispatch(%s) succeeded, want protocol error", line)
		}
	}
}

func TestSTTMapsCumulativeSamplesOntoSourcePTS(t *testing.T) {
	t.Parallel()

	session := &sttSession{
		events: make(chan engine.Transcript, 1), stop: make(chan struct{}),
		log: slog.New(slog.DiscardHandler), lang: "it",
	}
	session.readySeen.Store(true)
	session.timeline.record(core.PCM{
		Data: make([]int16, 160), Rate: 16000, Ch: 1, PTS: 2 * time.Second,
	})
	// A discontinuity proves the adapter maps samples through pushed-frame PTS
	// rather than assuming that cumulative samples always begin at source zero.
	session.timeline.record(core.PCM{
		Data: make([]int16, 160), Rate: 16000, Ch: 1, PTS: 3 * time.Second,
	})

	if err := session.dispatch([]byte(`{"partial":"ciao","end_samples":240}`)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := <-session.events
	if !got.HasSourceEnd || got.SourceEnd != 3005*time.Millisecond {
		t.Fatalf("source timing = (%v, %v), want (3.005s, true)", got.SourceEnd, got.HasSourceEnd)
	}
}

func TestSTTRejectsImpossibleOrRegressingSampleBoundaries(t *testing.T) {
	t.Parallel()

	session := &sttSession{
		events: make(chan engine.Transcript, 2), stop: make(chan struct{}),
		log: slog.New(slog.DiscardHandler), lang: "it",
	}
	session.readySeen.Store(true)
	session.timeline.record(core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1})
	if err := session.dispatch([]byte(`{"partial":"ciao","end_samples":200}`)); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if err := session.dispatch([]byte(`{"partial":"ciao","end_samples":199}`)); err == nil ||
		!strings.Contains(err.Error(), "moved backward") {
		t.Fatalf("backward timing error = %v", err)
	}
	if err := session.dispatch([]byte(`{"partial":"ciao","end_samples":321}`)); err == nil ||
		!strings.Contains(err.Error(), "exceeds 320") {
		t.Fatalf("future timing error = %v", err)
	}
}

func TestSourceTimelineCoalescesContinuousFramesButKeepsDiscontinuities(t *testing.T) {
	t.Parallel()

	const (
		rate         = 16000
		frameSamples = 320
		frameCount   = 10000
	)
	var timeline sourceTimeline
	for i := range frameCount {
		timeline.record(core.PCM{
			Data: make([]int16, frameSamples), Rate: rate, Ch: 1,
			PTS: time.Duration(i) * 20 * time.Millisecond,
		})
	}
	if got := len(timeline.frames); got != 1 {
		t.Fatalf("continuous timeline spans = %d, want 1", got)
	}

	timeline.record(core.PCM{
		Data: make([]int16, frameSamples), Rate: rate, Ch: 1,
		PTS: frameCount*20*time.Millisecond + time.Second,
	})
	if got := len(timeline.frames); got != 2 {
		t.Fatalf("timeline spans after PTS discontinuity = %d, want 2", got)
	}
}

func TestSTTRejectsTranscriptBeforeReadyHandshake(t *testing.T) {
	t.Parallel()

	session := &sttSession{
		ready: make(chan struct{}), events: make(chan engine.Transcript, 1),
		stop: make(chan struct{}), log: slog.New(slog.DiscardHandler), lang: "it",
	}
	err := session.dispatch([]byte(`{"partial":"ciao","end_samples":0}`))
	if err == nil || !strings.Contains(err.Error(), "before ready") {
		t.Fatalf("dispatch error = %v, want ordering violation", err)
	}
	if len(session.events) != 0 {
		t.Fatal("pre-readiness transcript leaked into events")
	}
}

func TestSTTReadyHandshakeIsSingleUse(t *testing.T) {
	t.Parallel()

	session := &sttSession{
		ready: make(chan struct{}), events: make(chan engine.Transcript, 1),
		stop: make(chan struct{}), log: slog.New(slog.DiscardHandler), lang: "it",
	}
	if err := session.dispatch([]byte(`{"ready":true}`)); err != nil {
		t.Fatalf("first ready: %v", err)
	}
	select {
	case <-session.ready:
	default:
		t.Fatal("ready handshake did not release startup")
	}
	if err := session.dispatch([]byte(`{"ready":true}`)); err == nil {
		t.Fatal("duplicate ready handshake succeeded")
	}
}

func TestSTTProtocolFailureStopsLiveHelperAndSurfacesError(t *testing.T) {
	t.Parallel()

	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeBadSTT, Rate: 16000,
	}).Open(t.Context(), "it")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for event := range transcription.Events() {
			_ = event
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("malformed STT response left the live helper blocked")
	}
	if terminalErr := transcription.Err(); terminalErr == nil ||
		!strings.Contains(terminalErr.Error(), "response JSON") {
		t.Fatalf("terminal error = %v, want malformed-response context", terminalErr)
	}
}

func TestSTTUnexpectedEOFStopsLiveHelperAndSurfacesError(t *testing.T) {
	t.Parallel()

	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeEOFSTT, Rate: 16000,
	}).Open(t.Context(), "it")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for event := range transcription.Events() {
			_ = event
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stdout EOF left the live helper blocked")
	}
	if terminalErr := transcription.Err(); !errors.Is(terminalErr, io.ErrUnexpectedEOF) {
		t.Fatalf("terminal error = %v, want unexpected EOF", terminalErr)
	}
}

func TestSTTWriteFailureWaitsForHelperDiagnostic(t *testing.T) {
	transcription, _ := openRejectingSTT(t.Context(), t)

	started := time.Now()
	err := transcription.Push(testPCMFrame())
	if err == nil || !strings.Contains(err.Error(), fakeSTTReject) {
		t.Fatalf("Push error = %v, want helper stderr", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Push took %v; failed helper was not stopped promptly", elapsed)
	}

	if events := collect(transcription.Events()); len(events) != 0 {
		t.Fatalf("failed helper produced events: %+v", events)
	}
	if terminalErr := transcription.Err(); terminalErr == nil ||
		!strings.Contains(terminalErr.Error(), fakeSTTReject) {
		t.Fatalf("terminal error = %v, want helper stderr", terminalErr)
	}
}

func TestSTTWriteFailureSurvivesConcurrentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	transcription, session := openRejectingSTT(ctx, t)

	result := make(chan error, 1)
	go func() { result <- transcription.Push(testPCMFrame()) }()
	waitSTTWriteFailure(t, session)
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Push error = %v, want context canceled", err)
	}
	if events := collect(transcription.Events()); len(events) != 0 {
		t.Fatalf("failed helper produced events: %+v", events)
	}
	if terminalErr := transcription.Err(); terminalErr == nil ||
		!strings.Contains(terminalErr.Error(), fakeSTTReject) {
		t.Fatalf("terminal error = %v, want retained helper stderr", terminalErr)
	}
}

func openRejectingSTT(
	ctx context.Context, t *testing.T,
) (engine.Transcription, *sttSession) {
	t.Helper()

	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeRejectSTT, Rate: 16000,
	}).Open(ctx, "it")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transcription.Close(); closeErr != nil {
			t.Errorf("Close returned error: %v", closeErr)
		}
	})

	session, ok := transcription.(*sttSession)
	if !ok {
		t.Fatalf("transcription type = %T, want *sttSession", transcription)
	}
	waitStderrMarker(t, session.stderr, fakeSTTReject)

	return transcription, session
}

func testPCMFrame() core.PCM {
	return core.PCM{Data: []int16{1, -1}, Rate: 16000, Ch: 1}
}

func waitSTTWriteFailure(t *testing.T, session *sttSession) {
	t.Helper()

	testkit.Eventually(t, 2*time.Second, func() bool {
		session.errMu.Lock()
		defer session.errMu.Unlock()

		return session.writeErr != nil
	}, "helper write never failed")
}

func TestAdaptersReportMissingBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-engine")

	if _, err := NewSTT(&STTConfig{Bin: missing, Model: fakeModel, Rate: 16000}).
		Open(t.Context(), "it"); err == nil || !strings.Contains(err.Error(), "start native stt") {
		t.Fatalf("STT start error = %v", err)
	}

	if _, err := NewMT(&MTConfig{Bin: missing}).Translate(
		t.Context(), engine.Segment{Text: "ciao", Lang: "it"}, "en",
	); err == nil || !strings.Contains(err.Error(), "start native mt") {
		t.Fatalf("MT start error = %v", err)
	}

	text := make(chan string)
	close(text)
	if _, err := NewTTS(&TTSConfig{Bin: missing, Rate: 16000}).Speak(
		t.Context(), "en", core.Voice{ID: fakeVoice}, text,
	); err == nil || !strings.Contains(err.Error(), "start native tts") {
		t.Fatalf("TTS start error = %v", err)
	}
}

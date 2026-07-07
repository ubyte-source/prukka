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

	transcription, err := NewSTT(&STTConfig{Bin: os.Args[0], Model: fakeModel, Rate: 16000}).
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
		{Text: "buon", Lang: "it"},
		{Text: "buongiorno", Lang: "it"},
		{Text: "Buongiorno a tutti.", Lang: "it", Stable: true, Final: true},
		{Text: "il ponte", Lang: "it"},
		{Text: "Il ponte è aperto.", Lang: "it", Stable: true, Final: true},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", got, want)
	}
	if terminalErr := transcription.Err(); terminalErr != nil {
		t.Fatalf("terminal error after clean stream = %v, want nil", terminalErr)
	}
}

func TestSTTArgs(t *testing.T) {
	t.Parallel()

	stt := NewSTT(&STTConfig{Model: "m", Rate: 16000, Threads: 2})

	got := stt.args(core.Lang("it-IT"))
	want := []string{
		"stt", "--model", "m", "--rate", "16000", "--threads", "2", "--language", "it",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}

	gotAuto := stt.args(core.LangAuto)
	wantAuto := []string{"stt", "--model", "m", "--rate", "16000", "--threads", "2"}
	if !reflect.DeepEqual(gotAuto, wantAuto) {
		t.Fatalf("auto args = %v, want %v", gotAuto, wantAuto)
	}

	defaultThreads := NewSTT(&STTConfig{Model: "m", Rate: 16000}).args(core.LangAuto)
	if got := flagValue(defaultThreads, flagThreads); got != "1" {
		t.Fatalf("default threads = %q, want 1", got)
	}
}

func TestTailBufferKeepsLastBytes(t *testing.T) {
	t.Parallel()

	tail := &tailBuffer{limit: 4}
	for _, chunk := range []string{"abc", "defg", "hi"} {
		if _, err := tail.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if got := tail.String(); got != "fghi" {
		t.Fatalf("tail = %q, want %q", got, "fghi")
	}

	if _, err := tail.Write([]byte("0123456789")); err != nil {
		t.Fatalf("oversized write: %v", err)
	}
	if got := tail.String(); got != "6789" {
		t.Fatalf("tail after oversized write = %q, want %q", got, "6789")
	}

	disabled := &tailBuffer{}
	if n, err := disabled.Write([]byte("ignored")); err != nil || n != len("ignored") {
		t.Fatalf("disabled tail write = (%d, %v)", n, err)
	}
	if got := disabled.String(); got != "" {
		t.Fatalf("disabled tail = %q, want empty", got)
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

	err := session.dispatch([]byte(
		`{"partial":"ciao","language":"../../models/private"}`,
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
	} {
		session := &sttSession{
			events: make(chan engine.Transcript, 1), stop: make(chan struct{}),
			log: slog.New(slog.DiscardHandler), lang: "it",
		}
		if err := session.dispatch([]byte(line)); err == nil {
			t.Errorf("dispatch(%s) succeeded, want protocol error", line)
		}
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

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		session.errMu.Lock()
		failed := session.writeErr != nil
		session.errMu.Unlock()
		if failed {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("wait for STT write failure: %v", ctx.Err())
		case <-ticker.C:
		}
	}
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

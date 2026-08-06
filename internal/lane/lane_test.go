package lane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

type failingIngress struct{ err error }

func (i failingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	return nil, i.err
}

type blockingReadyTranscriber struct {
	started chan struct{}
	ready   chan struct{}
}

func (t blockingReadyTranscriber) Open(
	ctx context.Context, _ core.Lang,
) (realtime.Transcription, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.ready:
		return newEmptyTranscription(), nil
	}
}

type closeProbeTranslator struct{ closed int }

func (*closeProbeTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (*closeProbeTranslator) Translate(
	context.Context, realtime.Segment, core.Lang,
) (string, error) {
	return "", nil
}

func (p *closeProbeTranslator) Close() error {
	p.closed++

	return nil
}

type closeProbeSynth struct{ closed int }

func (*closeProbeSynth) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*realtime.AudioStream, error) {
	return nil, errors.New("unexpected synthesis")
}

func (p *closeProbeSynth) Close() error {
	p.closed++

	return nil
}

// TestNewStarterRefusesNonMediaProfilesBeforeTakingASlot: an unsupported
// profile costs no concurrency.
func TestNewStarterRefusesNonMediaProfilesBeforeTakingASlot(t *testing.T) {
	t.Parallel()

	slots := semaphore.NewWeighted(1)
	start := NewStarter(&StarterDeps{Slots: slots, Log: discard()})

	err := start(t.Context(), &session.Session{Slug: "web-only", Profile: "web"}, func() {})
	if err == nil || !strings.Contains(err.Error(), "does not support media lanes") {
		t.Fatalf("starter on an unsupported profile = %v, want the profile refusal", err)
	}
	if !slots.TryAcquire(1) {
		t.Fatal("the refused start held on to a lane slot")
	}
}

func TestRunLaneClosesProvidersOnStartupFailure(t *testing.T) {
	translator := &closeProbeTranslator{}
	synth := &closeProbeSynth{}
	log := discard()
	errIngress := errors.New("capture unavailable")
	d := &runDeps{
		session: &session.Session{
			Slug:    "close-providers",
			Profile: session.ProfileBroadcast,
			Source:  core.SourceSpec{URL: "file:///missing.wav"},
			Langs:   []core.Lang{"it"},
		},
		transcriber: emptyTranscriber{},
		translator:  translator,
		synth:       synth,
		ingress:     failingIngress{err: errIngress},
		out: Outputs{
			vtt:   vtt.NewRegistry(),
			audio: audio.NewRegistry(t.Context(), nil, nil, log),
			hls:   hls.NewStore(t.TempDir(), log),
		},
		log:    log,
		voices: []core.Voice{{ID: "voice", Lang: "it"}},
	}

	err := run(t.Context(), d, func() {})
	if !errors.Is(err, errIngress) {
		t.Fatalf("run error = %v, want ingress failure", err)
	}
	if translator.closed != 1 || synth.closed != 1 {
		t.Fatalf("provider closes = translator:%d synth:%d, want one each", translator.closed, synth.closed)
	}
}

func TestRunLaneWaitsForTranscriptionBeforeOpeningIngress(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	ready := make(chan struct{})
	opened := make(chan struct{})
	frames := &scriptedFrames{results: []frameResult{
		{frame: core.PCM{Data: make([]int16, 320), Rate: 16_000, Ch: 1}},
		{err: io.EOF},
	}}
	log := discard()
	var startupLogs bytes.Buffer
	s := &session.Session{
		Slug:       "ready-before-capture",
		Profile:    session.ProfileCall,
		Source:     core.SourceSpec{URL: "device://audio/microphone?token=lane-secret"},
		Langs:      []core.Lang{"it"},
		SourceLang: "it",
		Subs:       session.SubsOff,
		Bed:        session.Off,
	}
	startup := startupObserverForTest(&startupLogs, s, 100*time.Millisecond)
	d := &runDeps{
		session:     s,
		transcriber: blockingReadyTranscriber{started: started, ready: ready},
		ingress:     recordingIngress{frames: frames, opened: opened},
		out: Outputs{
			vtt:   vtt.NewRegistry(),
			audio: audio.NewRegistry(t.Context(), nil, nil, log),
			hls:   hls.NewStore(t.TempDir(), log),
		},
		log:     log,
		startup: startup,
	}

	runningSignals := 0
	done := make(chan error, 1)
	go func() { done <- run(t.Context(), d, func() { runningSignals++ }) }()
	<-started
	select {
	case <-opened:
		t.Fatal("ingress opened before transcription readiness")
	default:
	}

	close(ready)
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("ingress did not open after transcription became ready")
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if runningSignals != 1 {
		t.Fatalf("running signals = %d, want one", runningSignals)
	}

	assertReadyBeforeCaptureLogs(t, &startupLogs)
}

// assertReadyBeforeCaptureLogs pins the four startup phases in order, both
// measured durations, and no secret carried in from the source URL.
func assertReadyBeforeCaptureLogs(t *testing.T, startupLogs *bytes.Buffer) {
	t.Helper()

	entries := decodeStartupLogs(t, startupLogs.Bytes())
	assertStartupPhases(
		t, entries,
		"waiting_for_media", "transcription_warming", "transcription_ready", "media_ready",
	)
	if got := entries[2]["phase_duration_ms"]; got != float64(100) {
		t.Fatalf("transcription warm duration = %v, want 100 ms", got)
	}
	if got := entries[3]["phase_duration_ms"]; got != float64(300) {
		t.Fatalf("media-ready duration = %v, want 300 ms", got)
	}
	assertLogOmits(t, startupLogs.String(), "lane-secret", "device://")
}

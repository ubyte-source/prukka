package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	fileingress "github.com/ubyte-source/prukka/internal/media/ingest/file"
	"github.com/ubyte-source/prukka/internal/providers/native"
	"github.com/ubyte-source/prukka/internal/speech"
)

// stubEnginePath is a non-empty local engine path so installedEngine treats the
// bundled engine as present without a real binary on disk.
const stubEnginePath = "stub-engine"

type scriptedFrames struct {
	closeErr error
	results  []frameResult
	closed   int
}

type frameResult struct {
	err   error
	frame core.PCM
}

type failingIngress struct{ err error }

type emptyTranscriber struct{}

type blockingReadyTranscriber struct {
	started chan struct{}
	ready   chan struct{}
}

type emptyTranscription struct {
	events chan engine.Transcript
	once   sync.Once
}

func (emptyTranscriber) Open(context.Context, core.Lang) (engine.Transcription, error) {
	return newEmptyTranscription(), nil
}

func (t blockingReadyTranscriber) Open(
	ctx context.Context, _ core.Lang,
) (engine.Transcription, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.ready:
		return newEmptyTranscription(), nil
	}
}

func newEmptyTranscription() *emptyTranscription {
	return &emptyTranscription{events: make(chan engine.Transcript)}
}

func (*emptyTranscription) Push(core.PCM) error { return nil }

func (t *emptyTranscription) Events() <-chan engine.Transcript { return t.events }

func (*emptyTranscription) Err() error { return nil }

func (t *emptyTranscription) CloseSend() error {
	t.close()

	return nil
}

func (t *emptyTranscription) Close() error {
	t.close()

	return nil
}

func (t *emptyTranscription) close() { t.once.Do(func() { close(t.events) }) }

type recordingIngress struct {
	frames core.Frames
	opened chan struct{}
}

func (i recordingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	close(i.opened)

	return i.frames, nil
}

type blockingIngress struct {
	frames  core.Frames
	started chan struct{}
	release chan struct{}
}

func (i blockingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	close(i.started)
	<-i.release

	return i.frames, nil
}

type closeTrackedFrames struct {
	closeErr error
	mu       sync.Mutex
	closes   int
	nexts    int
}

func (f *closeTrackedFrames) Next(context.Context) (core.PCM, error) {
	f.mu.Lock()
	f.nexts++
	f.mu.Unlock()

	return core.PCM{}, io.EOF
}

func (f *closeTrackedFrames) Close() error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()

	return f.closeErr
}

func (f *closeTrackedFrames) counts() (closes, nexts int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closes, f.nexts
}

type recordingMTWarmer struct{ calls chan string }

func (w recordingMTWarmer) Warm(_ context.Context, from, to core.Lang) error {
	w.calls <- string(from) + ">" + string(to)

	return nil
}

type recordingTTSWarmer struct{ calls chan string }

type blockingMTWarmer struct{}

func (blockingMTWarmer) Warm(ctx context.Context, _, _ core.Lang) error {
	<-ctx.Done()

	return ctx.Err()
}

type failingMTWarmer struct{ err error }

func (w failingMTWarmer) Warm(context.Context, core.Lang, core.Lang) error { return w.err }

type steppingClock struct {
	now  time.Time
	step time.Duration
	mu   sync.Mutex
}

func (c *steppingClock) tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)

	return c.now
}

type concurrencyMTWarmer struct {
	release chan struct{}
	started chan struct{}
	mu      sync.Mutex
	active  int
	maximum int
}

func (w *concurrencyMTWarmer) Warm(ctx context.Context, _, _ core.Lang) error {
	w.mu.Lock()
	w.active++
	w.maximum = max(w.maximum, w.active)
	w.mu.Unlock()
	w.started <- struct{}{}
	defer func() {
		w.mu.Lock()
		w.active--
		w.mu.Unlock()
	}()

	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *concurrencyMTWarmer) maxConcurrent() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.maximum
}

func (w recordingTTSWarmer) Warm(_ context.Context, to core.Lang, voice core.Voice) error {
	w.calls <- string(to) + ">" + voice.ID

	return nil
}

func (i failingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	return nil, i.err
}

type closeProbeTranslator struct{ closed int }

func (*closeProbeTranslator) Supports(core.Lang, core.Lang) bool { return true }

func (*closeProbeTranslator) Translate(
	context.Context, engine.Segment, core.Lang,
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
) (*engine.AudioStream, error) {
	return nil, errors.New("unexpected synthesis")
}

func (p *closeProbeSynth) Close() error {
	p.closed++

	return nil
}

func (f *scriptedFrames) Next(context.Context) (core.PCM, error) {
	result := f.results[0]
	f.results = f.results[1:]

	return result.frame, result.err
}

func (f *scriptedFrames) Close() error {
	f.closed++

	return f.closeErr
}

func TestObservedFramesSignalsOnlyAfterMediaFlows(t *testing.T) {
	t.Parallel()

	signals := 0
	wantCloseErr := errors.New("close source")
	source := &scriptedFrames{results: []frameResult{
		{err: errors.New("not ready")},
		{frame: core.PCM{Data: []int16{1}, Rate: 16_000, Ch: 1}},
		{err: io.EOF},
	}, closeErr: wantCloseErr}
	frames := &observedFrames{
		Frames:  source,
		running: func() { signals++ },
	}

	if _, err := frames.Next(t.Context()); err == nil || signals != 0 {
		t.Fatalf("failed read = %v, signals = %d; want error and no running signal", err, signals)
	}
	if _, err := frames.Next(t.Context()); err != nil || signals != 1 {
		t.Fatalf("media read = %v, signals = %d; want success and one signal", err, signals)
	}
	if _, err := frames.Next(t.Context()); !errors.Is(err, io.EOF) || signals != 1 {
		t.Fatalf("EOF read = %v, signals = %d; want EOF and one signal", err, signals)
	}
	if err := frames.Close(); !errors.Is(err, wantCloseErr) || source.closed != 1 {
		t.Fatalf("Close = %v, wrapped closes = %d; want wrapped error and one", err, source.closed)
	}
}

func TestRunEngineLaneClosesProvidersOnStartupFailure(t *testing.T) {
	translator := &closeProbeTranslator{}
	synth := &closeProbeSynth{}
	log := discard()
	errIngress := errors.New("capture unavailable")
	d := &laneDeps{
		session: &session.Session{
			Slug: "close-providers", Profile: session.ProfileBroadcast,
			Source: core.SourceSpec{URL: "file:///missing.wav"}, Langs: []core.Lang{"it"},
		},
		transcriber: emptyTranscriber{},
		translator:  translator,
		synth:       synth,
		ingress:     failingIngress{err: errIngress},
		out: laneOutputs{
			vtt:   vtt.NewRegistry(),
			audio: audio.NewRegistry(t.Context(), nil, nil, log),
			hls:   hls.NewStore(t.TempDir(), log),
		},
		log: log, voices: []core.Voice{{ID: "voice", Lang: "it"}},
	}

	err := runEngineLane(t.Context(), d, func() {})
	if !errors.Is(err, errIngress) {
		t.Fatalf("runEngineLane error = %v, want ingress failure", err)
	}
	if translator.closed != 1 || synth.closed != 1 {
		t.Fatalf("provider closes = translator:%d synth:%d, want one each", translator.closed, synth.closed)
	}
}

func TestRunEngineLaneWaitsForTranscriptionBeforeOpeningIngress(t *testing.T) {
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
		Slug: "ready-before-capture", Profile: session.ProfileCall,
		Source: core.SourceSpec{URL: "device://audio/microphone?token=lane-secret"},
		Langs:  []core.Lang{"it"},
		Flags:  map[string]string{"source": "it", "subs": "off", "bed": "off"},
	}
	startup := startupObserverForTest(&startupLogs, s, 100*time.Millisecond)
	d := &laneDeps{
		session:     s,
		transcriber: blockingReadyTranscriber{started: started, ready: ready},
		ingress:     recordingIngress{frames: frames, opened: opened},
		out: laneOutputs{
			vtt:   vtt.NewRegistry(),
			audio: audio.NewRegistry(t.Context(), nil, nil, log),
			hls:   hls.NewStore(t.TempDir(), log),
		},
		log: log, startup: startup,
	}

	runningSignals := 0
	done := make(chan error, 1)
	go func() { done <- runEngineLane(t.Context(), d, func() { runningSignals++ }) }()
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
		t.Fatalf("runEngineLane: %v", err)
	}
	if runningSignals != 1 {
		t.Fatalf("running signals = %d, want one", runningSignals)
	}

	entries := decodeStartupLogs(t, startupLogs.Bytes())
	wantPhases := []string{
		"waiting_for_media", "transcription_warming", "transcription_ready", "media_ready",
	}
	assertStartupPhases(t, entries, wantPhases...)
	if got := entries[2]["phase_duration_ms"]; got != float64(100) {
		t.Fatalf("transcription warm duration = %v, want 100 ms", got)
	}
	if got := entries[3]["phase_duration_ms"]; got != float64(300) {
		t.Fatalf("media-ready duration = %v, want 300 ms", got)
	}
	assertLogOmits(t, startupLogs.String(), "lane-secret", "device://")
}

func TestLazyFramesCancellationClosesSourceReturnedByInFlightOpen(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("eventual close")
	source := &closeTrackedFrames{closeErr: closeErr}
	ingress := blockingIngress{
		frames: source, started: make(chan struct{}), release: make(chan struct{}),
	}
	frames := newLazyFrames(ingress, core.SourceSpec{URL: "device://audio/microphone"})
	ctx, cancel := context.WithCancel(context.Background())
	nextDone := make(chan error, 1)
	go func() {
		_, err := frames.Next(ctx)
		nextDone <- err
	}()
	<-ingress.started
	cancel()

	closeDone := make(chan error, 1)
	go func() { closeDone <- frames.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close during Open = %v, want no source result yet", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on an in-flight ingress Open")
	}

	close(ingress.release)
	if err := <-nextDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after cancellation = %v, want context.Canceled", err)
	}
	if err := frames.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close after eventual source = %v, want %v", err, closeErr)
	}
	if closes, nexts := source.counts(); closes != 1 || nexts != 0 {
		t.Fatalf("eventual source lifecycle = %d closes/%d reads, want 1/0", closes, nexts)
	}
}

func TestIngressForKeepsLoopingWAVOnTheNativeReader(t *testing.T) {
	t.Parallel()

	ingress, err := ingressFor(
		"file:///tmp/take.WAV?loop=true", session.ProfileCall, slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("ingressFor returned error: %v", err)
	}
	if _, ok := ingress.(fileingress.Ingress); !ok {
		t.Fatalf("ingress type = %T, want native WAV ingress", ingress)
	}
}

func TestBedLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		flag string
		want float64
	}{
		{flag: "-15dB", want: -15},
		{flag: "-9dB", want: -9},
		{flag: "-20", want: -20},
		{flag: "  -12dB ", want: -12},
		{flag: "", want: -9},
		{flag: "off", want: math.Inf(-1)},
	}

	for _, tc := range cases {
		if got := bedLevel(tc.flag, -9); got != tc.want {
			t.Errorf("bedLevel(%q) = %v, want %v", tc.flag, got, tc.want)
		}
	}
}

func TestSourceHint(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		flags map[string]string
		want  core.Lang
	}{
		{name: "absent", flags: nil, want: core.LangAuto},
		{name: "valid", flags: map[string]string{"source": "it"}, want: "it"},
		{name: "region", flags: map[string]string{"source": "de-CH"}, want: "de-CH"},
		{name: "invalid falls back to auto", flags: map[string]string{"source": "nope"}, want: core.LangAuto},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := &session.Session{Flags: tc.flags}
			if got := sourceHint(s); got != tc.want {
				t.Fatalf("sourceHint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfiguredSessionCapabilityChecksDirectedModels(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}
	validate := configuredSessionCapability(holder)

	for _, tc := range []struct {
		name    string
		source  string
		wantErr string
		targets []core.Lang
	}{
		{name: "declared pair", source: "it-IT", targets: []core.Lang{"en"}},
		{name: "declared reverse pair", source: "en", targets: []core.Lang{"it"}},
		{name: "same language", source: "en", targets: []core.Lang{"en-US"}},
		{name: "auto deferred", targets: []core.Lang{"de"}},
		{name: "undeclared direction", source: "en", targets: []core.Lang{"de"}, wantErr: "en to de"},
	} {
		sess := &session.Session{Langs: tc.targets, Flags: map[string]string{}}
		if tc.source != "" {
			sess.Flags["source"] = tc.source
		}
		err := validate(sess)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestConfiguredSessionCapabilityBridgesThroughHub proves admission judges a
// pinned source the same way the runtime translator does: a session whose
// direct pair is absent but whose hub legs are installed (it->en->de) is
// admitted, while one with no hub path (it->fr, no en<->fr) is still rejected.
func TestConfiguredSessionCapabilityBridgesThroughHub(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Local.MT.Pairs = []config.TranslationPair{
		{From: "it", To: "en"}, {From: "en", To: "it"},
		{From: "de", To: "en"}, {From: "en", To: "de"},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}
	validate := configuredSessionCapability(holder)

	for _, tc := range []struct {
		name    string
		source  string
		wantErr string
		targets []core.Lang
	}{
		{name: "pinned source bridges it->en->de", source: "it", targets: []core.Lang{"de"}},
		{name: "pinned source bridges de->en->it", source: "de", targets: []core.Lang{"it"}},
		{name: "direct spoke from hub", source: "en", targets: []core.Lang{"de"}},
		{name: "no hub leg to french", source: "it", targets: []core.Lang{"fr"}, wantErr: "it to fr"},
		{name: "multi-target rejects the unbridged one", source: "it", targets: []core.Lang{"de", "fr"}, wantErr: "it to fr"},
	} {
		sess := &session.Session{Langs: tc.targets, Flags: map[string]string{}}
		if tc.source != "" {
			sess.Flags["source"] = tc.source
		}
		err := validate(sess)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestConfiguredDubbedLanguagesSpansEveryVoice: a bidirectional bundle voices
// every target it has a voice for, so both call directions are dubbable.
func TestConfiguredDubbedLanguagesSpansEveryVoice(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Local.TTS.Voices = []config.VoiceModel{
		{Language: "en", Voice: "models/tts/en.onnx"},
		{Language: "it", Voice: "models/tts/it.onnx"},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}

	dubbed := configuredDubbedLanguages(holder)
	sess := &session.Session{Langs: []core.Lang{"en", "it", "de"}, Flags: map[string]string{}}
	if got := dubbed(sess); len(got) != 2 || got[0] != "en" || got[1] != "it" {
		t.Fatalf("configuredDubbedLanguages = %v, want [en it] (de has no voice)", got)
	}
}

func TestDubbedLanguagesSelectsASubset(t *testing.T) {
	t.Parallel()

	s := &session.Session{Langs: []core.Lang{"it", "de", "en"}, Flags: map[string]string{"dub_langs": "DE,it"}}
	got := dubbedLanguages(s)
	if len(got) != 2 || got[0] != "it" || got[1] != "de" {
		t.Fatalf("dubbedLanguages = %v, want session-order [it de]", got)
	}

	s.Flags["dub"] = "off"
	if got := dubbedLanguages(s); len(got) != 0 {
		t.Fatalf("dub=off selected %v, want none", got)
	}
}

func TestCaptionSinksDiscardWhenSubtitlesAreOff(t *testing.T) {
	t.Parallel()

	registry := vtt.NewRegistry()
	s := &session.Session{
		Slug: "dub-only", Langs: []core.Lang{"it"}, Flags: map[string]string{"subs": "off"},
	}
	sinks := captionSinks(s, &laneDeps{out: laneOutputs{vtt: registry}}, nil)
	sinks["it"].Append(&core.TranslatedSegment{Text: "ciao"})

	if _, ok := registry.Document("dub-only", "it"); ok {
		t.Fatal("subs=off registered a direct WebVTT document")
	}
}

func TestCallProfileSkipsRollingCaptionMedia(t *testing.T) {
	t.Parallel()

	s := &session.Session{
		Slug: "fast-call", Profile: session.ProfileCall, Langs: []core.Lang{"en"},
		Source: core.SourceSpec{URL: "device://audio/microphone"},
	}
	if media := createCaptionMedia(s, &laneDeps{}); media != nil {
		t.Fatalf("call media = %v, want no HLS tree", media)
	}
	av := &session.Session{
		Profile: session.ProfileCall, Source: core.SourceSpec{URL: "device://av/camera"},
	}
	if skipCaptionMedia(av) {
		t.Fatal("AV call skipped the video rendition needed by device pushes")
	}
}

// TestNewTranscriberRequiresInstalledEngine: transcription runs on the bundled
// local engine and fails clearly until it is installed. The state dir is
// hermetic so a managed bundle on the host cannot satisfy the fallback.
func TestNewTranscriberRequiresInstalledEngine(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	installed := config.Default()
	installed.Providers.Local.Bin = stubEnginePath
	if transcriber, err := newTranscriber(
		installed, session.ProfileBroadcast, nil, discard(),
	); err != nil || transcriber == nil {
		t.Fatalf("transcriber = (%v, %v), want a port", transcriber, err)
	}

	missing := config.Default()
	if _, err := newTranscriber(missing, session.ProfileBroadcast, nil, discard()); err == nil {
		t.Fatal("transcriber without an installed engine must fail")
	}
}

// TestInstalledEngineFallsBackToManagedBundle: with providers.local.bin
// unset, a complete managed install under the state dir serves as the engine —
// the daemon self-executes, so Bin is this binary and the bundle root is
// threaded through as the engine root; the shared config snapshot stays
// untouched.
func TestInstalledEngineFallsBackToManagedBundle(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("PRUKKA_STATE", stateDir)

	inventory := `{"schema":"prukka.engine.state","version":1,"protocol":2,` +
		`"runtime":{"os":"any","arch":"any","sha256":"x"},"packs":[]}`
	writeEngineFixture(t, filepath.Join(stateDir, "engine", "state.json"), []byte(inventory), 0o600)
	writeManagedNativeTools(t, speech.BundleRoot(stateDir))

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}

	cfg := config.Default()
	local, engineRoot, err := installedEngine(&cfg.Providers)
	if err != nil || local.Bin != self || engineRoot != speech.BundleRoot(stateDir) {
		t.Fatalf("managed fallback = (%+v, %q, %v); want Bin=%q root=%q",
			local, engineRoot, err, self, speech.BundleRoot(stateDir))
	}
	if cfg.Providers.Local.Bin != "" {
		t.Fatal("fallback mutated the shared config snapshot")
	}

	assertManagedStagesSpawnResolvedBinary(t, cfg, self)
}

// writeManagedNativeTools plants the compiled helpers a managed bundle must
// carry so speech.Resolve accepts it; the daemon self-executes against them.
func writeManagedNativeTools(t *testing.T, root string) {
	t.Helper()

	tools := []string{"whisper-server", "mt", filepath.Join("piper", "piper")}
	for _, tool := range tools {
		if runtime.GOOS == "windows" {
			tool += ".exe"
		}
		writeEngineFixture(t, filepath.Join(root, tool), []byte("tool"), 0o700)
	}
	if runtime.GOOS == "darwin" {
		// The darwin bundle also ships the microphone-capture helper.
		writeEngineFixture(t, filepath.Join(root, "prukka-miccapture"), []byte("tool"), 0o700)
	}
}

// assertManagedStagesSpawnResolvedBinary pins every stage constructor to the
// resolved self-exec binary, not the empty configured one: a blank Bin
// surfaces only at spawn time as "exec: no command".
func assertManagedStagesSpawnResolvedBinary(t *testing.T, cfg *config.Config, wantBin string) {
	t.Helper()

	synth, voices, err := newSynthesizer(cfg, discard())
	if err != nil || synth == nil || len(voices) == 0 {
		t.Fatalf("managed synthesizer = (%v, %v, %v)", synth, voices, err)
	}
	if got := synth.SpawnPath(); got != wantBin {
		t.Fatalf("synthesizer spawns %q, want the managed binary", got)
	}
	transcriber, err := newTranscriber(cfg, session.ProfileCall, nil, discard())
	if err != nil || transcriber == nil {
		t.Fatalf("managed transcriber = (%v, %v)", transcriber, err)
	}
	translator, err := newTranslator(cfg)
	if err != nil || translator == nil {
		t.Fatalf("managed translator = (%v, %v)", translator, err)
	}
}

// writeEngineFixture plants one managed-install file with its parents.
func writeEngineFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("fixture dir: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("fixture file: %v", err)
	}
}

func TestSTTThreadsSharesCPUAcrossLanes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cpus     int
		maxLanes int
		want     int
	}{
		{name: "single lane", cpus: 4, maxLanes: 1, want: 4},
		{name: "two lanes", cpus: 4, maxLanes: 2, want: 2},
		{name: "one per CPU", cpus: 4, maxLanes: 4, want: 1},
		{name: "more lanes than CPUs", cpus: 4, maxLanes: 10, want: 1},
		{name: "single CPU", cpus: 1, maxLanes: 64, want: 1},
		{name: "per helper ceiling", cpus: 16, maxLanes: 2, want: 4},
		{name: "defensive zero CPUs", cpus: 0, maxLanes: 4, want: 1},
		{name: "defensive zero lanes", cpus: 4, maxLanes: 0, want: 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := sttThreads(test.cpus, test.maxLanes, session.ProfileBroadcast); got != test.want {
				t.Fatalf("sttThreads(%d, %d) = %d, want %d", test.cpus, test.maxLanes, got, test.want)
			}
		})
	}
}

func TestCallSTTThreadsShareOneBudgetPerConversation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cpus     int
		maxLanes int
		want     int
	}{
		{name: "thread ceiling", cpus: 16, maxLanes: 2, want: 8},
		{name: "one four-CPU pair", cpus: 4, maxLanes: 2, want: 4},
		{name: "two call pairs", cpus: 8, maxLanes: 4, want: 4},
		{name: "odd lane bound", cpus: 8, maxLanes: 3, want: 4},
	}
	for _, test := range tests {
		if got := sttThreads(test.cpus, test.maxLanes, session.ProfileCall); got != test.want {
			t.Errorf("%s: call STT threads = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestCallSTTUsesQualityTuning(t *testing.T) {
	t.Parallel()

	if got := sttTuning(session.ProfileBroadcast); got != (native.STTTuning{}) {
		t.Fatalf("broadcast tuning = %+v, want helper defaults", got)
	}
	got := sttTuning(session.ProfileCall)
	if got.SilenceHang != 300*time.Millisecond || got.MaxWindow != 5*time.Second ||
		got.MinSpeech != 250*time.Millisecond || got.PartialStride != 5*time.Second ||
		!got.FastDecode {
		t.Fatalf("call tuning = %+v", got)
	}
}

func TestSTTModelIsProfileScoped(t *testing.T) {
	t.Parallel()

	models := config.LocalSTT{
		Model: "models/stt/ggml-base.bin", CallModel: "models/stt/ggml-tiny-q5_1.bin",
	}
	if got := sttModel(models, session.ProfileBroadcast); got != models.Model {
		t.Fatalf("broadcast model = %q, want %q", got, models.Model)
	}
	if got := sttModel(models, session.ProfileCall); got != models.CallModel {
		t.Fatalf("call model = %q, want %q", got, models.CallModel)
	}

	models.CallModel = ""
	if got := sttModel(models, session.ProfileCall); got != models.Model {
		t.Fatalf("call fallback model = %q, want %q", got, models.Model)
	}
}

func TestCallProvidersWarmExactPairAndSelectedVoice(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 4)
	defer pool.Close()
	mtCalls := make(chan string, 4)
	ttsCalls := make(chan string, 4)
	s := &session.Session{
		Slug: "warm-call", Profile: session.ProfileCall, Langs: []core.Lang{"it", "en"},
		Flags: map[string]string{"source": "it", "dub_langs": "en"},
	}
	voices := []core.Voice{{ID: "voice-en", Lang: "en"}, {ID: "voice-it", Lang: "it"}}
	if err := warmCallProviders(
		t.Context(), callWarmTimeout, pool, s, recordingMTWarmer{calls: mtCalls},
		recordingTTSWarmer{calls: ttsCalls}, voices, nil,
	); err != nil {
		t.Fatalf("warmCallProviders: %v", err)
	}
	if got := <-mtCalls; got != "it>en" {
		t.Fatalf("warmed pair = %q, want it>en", got)
	}
	if got := <-ttsCalls; got != "en>voice-en" {
		t.Fatalf("warmed voice = %q, want en>voice-en", got)
	}
	select {
	case extra := <-mtCalls:
		t.Fatalf("extra MT warm %q", extra)
	default:
	}
	select {
	case extra := <-ttsCalls:
		t.Fatalf("extra TTS warm %q", extra)
	default:
	}
}

func TestCallProviderWarmupLogsStructuredDuration(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 4)
	defer pool.Close()
	s := &session.Session{
		Slug: "observable-warm", Profile: session.ProfileCall,
		Source: core.SourceSpec{URL: "file:///Users/alice/private.wav?token=source-secret"},
		Langs:  []core.Lang{"en"},
		Flags:  map[string]string{"source": "it", "dub_langs": "en"},
	}
	var logs bytes.Buffer
	observer := startupObserverForTest(&logs, s, 125*time.Millisecond)
	voices := []core.Voice{{ID: "voice-secret", Lang: "en"}}
	if err := warmCallProviders(
		t.Context(), callWarmTimeout, pool, s, recordingMTWarmer{calls: make(chan string, 1)},
		recordingTTSWarmer{calls: make(chan string, 1)}, voices, observer,
	); err != nil {
		t.Fatalf("warmCallProviders: %v", err)
	}

	entries := decodeStartupLogs(t, logs.Bytes())
	assertStartupPhases(t, entries, "providers_warming", "providers_ready")
	if entries[0]["mt_tasks"] != float64(1) || entries[0]["tts_tasks"] != float64(1) {
		t.Fatalf("provider task counts = %v, want one MT and one TTS", entries[0])
	}
	if got := entries[1]["phase_duration_ms"]; got != float64(125) {
		t.Fatalf("provider warm duration = %v, want 125 ms", got)
	}
	if got := entries[1]["startup_duration_ms"]; got != float64(250) {
		t.Fatalf("startup duration = %v, want 250 ms", got)
	}
	assertLogOmits(t, logs.String(), "/Users/alice", "source-secret", "voice-secret")
}

func TestCallProviderWarmupFailureLogOmitsProviderDetails(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(1, 1)
	defer pool.Close()
	s := &session.Session{
		Slug: "safe-warm-failure", Profile: session.ProfileCall, Langs: []core.Lang{"en"},
		Flags: map[string]string{"source": "it"},
	}
	var logs bytes.Buffer
	observer := startupObserverForTest(&logs, s, 10*time.Millisecond)
	warmErr := errors.New("open /Users/alice/models/private.bin: token=provider-secret")
	err := warmCallProviders(
		t.Context(), callWarmTimeout, pool, s, failingMTWarmer{err: warmErr}, nil, nil, observer,
	)
	if !errors.Is(err, warmErr) {
		t.Fatalf("warmCallProviders error = %v, want provider failure", err)
	}

	entries := decodeStartupLogs(t, logs.Bytes())
	if len(entries) != 2 || entries[1]["phase"] != "providers_failed" {
		t.Fatalf("provider failure logs = %v, want bounded failure phase", entries)
	}
	assertLogOmits(t, logs.String(), "/Users/alice", "private.bin", "provider-secret")
}

func TestCallProviderWarmupHasStartupDeadline(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(1, 1)
	defer pool.Close()
	s := &session.Session{
		Profile: session.ProfileCall, Langs: []core.Lang{"en"},
		Flags: map[string]string{"source": "it", "dub_langs": "en"},
	}
	started := time.Now()
	err := warmCallProviders(
		t.Context(), 20*time.Millisecond, pool, s, blockingMTWarmer{}, nil, nil, nil,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("warmup error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("warmup deadline returned after %s", elapsed)
	}
}

func TestCallProviderWarmupUsesSharedWorkerBound(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 1)
	defer pool.Close()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	warmer := &concurrencyMTWarmer{release: release, started: make(chan struct{}, 4)}
	s := &session.Session{
		Profile: session.ProfileCall, Langs: []core.Lang{"en", "de", "fr", "es"},
		Flags: map[string]string{"source": "it"},
	}

	result := make(chan error, 1)
	go func() {
		result <- warmCallProviders(t.Context(), time.Second, pool, s, warmer, nil, nil, nil)
	}()
	for range 2 {
		select {
		case <-warmer.started:
		case <-time.After(time.Second):
			t.Fatal("warmup did not use both available workers")
		}
	}
	select {
	case <-warmer.started:
		t.Fatal("warmup exceeded the shared two-worker limit")
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	if err := <-result; err != nil {
		t.Fatalf("warmCallProvidersWithin: %v", err)
	}
	if got := warmer.maxConcurrent(); got != 2 {
		t.Fatalf("maximum concurrent warmups = %d, want 2", got)
	}
}

// TestNewTranslatorRequiresInstalledEngine: translation runs on the bundled
// local engine and fails clearly until it is installed. The state dir is
// hermetic so a managed bundle on the host cannot satisfy the fallback.
func TestNewTranslatorRequiresInstalledEngine(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	installed := config.Default()
	installed.Providers.Local.Bin = stubEnginePath
	if translator, err := newTranslator(installed); err != nil || translator == nil {
		t.Fatalf("translator = (%v, %v), want a port", translator, err)
	}

	missing := config.Default()
	if _, err := newTranslator(missing); err == nil {
		t.Fatal("translator without an installed engine must fail")
	}
}

// TestNewSynthesizerSelectsVoiceStage: voices=off ships subtitles only; the
// bundled engine yields a synthesizer and preset voice, and fails until it is
// installed. The state dir is hermetic so a managed bundle on the host
// cannot satisfy the fallback.
func TestNewSynthesizerSelectsVoiceStage(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	subtitlesOnly := config.Default()
	subtitlesOnly.Providers.Voices = config.VoicesOff
	if synth, _, err := newSynthesizer(subtitlesOnly, discard()); err != nil || synth != nil {
		t.Fatalf("voices=off = (%v, %v), want no synthesizer", synth, err)
	}

	installed := config.Default()
	installed.Providers.Voices = config.VoicesLocal
	installed.Providers.Local.Bin = stubEnginePath
	installed.Providers.Local.TTS.Voices = []config.VoiceModel{{Language: "en", Voice: "preset"}}
	synth, voices, err := newSynthesizer(installed, discard())
	if err != nil || synth == nil || len(voices) != 1 || voices[0].ID != "preset" || voices[0].Lang != "en" {
		t.Fatalf("synthesizer = (%v, %v, %v), want a port and one en voice", synth, voices, err)
	}

	missing := config.Default()
	missing.Providers.Voices = config.VoicesLocal
	if _, _, synthErr := newSynthesizer(missing, discard()); synthErr == nil {
		t.Fatal("synthesizer without an installed engine must fail")
	}
}

// TestBuildEngineDub: no synthesizer is captions-only; with one the dub carries
// a bed and one track per dubbed language.
func TestBuildEngineDub(t *testing.T) {
	t.Parallel()

	s := &session.Session{Slug: "dub", Langs: []core.Lang{"it"}}
	registry := audio.NewRegistry(t.Context(), nil, nil, discard())

	if dub := buildEngineDub(s, &laneDeps{out: laneOutputs{audio: registry}}, nil); dub != nil {
		t.Fatalf("captions-only dub = %v, want none", dub)
	}

	deps := &laneDeps{synth: nopSynth{}, out: laneOutputs{audio: registry}, voices: []core.Voice{{ID: "v"}}}
	dub := buildEngineDub(s, deps, nil)
	if dub == nil || dub.Tracks["it"] == nil || dub.Voices["it"].ID != "v" || dub.Bed == nil {
		t.Fatalf("dub = %v, want a bed, a track and a voice per language", dub)
	}

	mixed := &session.Session{Slug: "mixed", Langs: []core.Lang{"it", "en-GB"}}
	englishOnly := &laneDeps{
		synth: nopSynth{}, out: laneOutputs{audio: registry},
		voices: []core.Voice{{ID: "en", Lang: "en"}}, log: discard(),
	}
	dub = buildEngineDub(mixed, englishOnly, nil)
	if dub == nil || dub.Tracks["en-GB"] == nil || dub.Tracks["it"] != nil {
		t.Fatalf("language-scoped dub tracks = %v, want only en-GB", dub)
	}

}

// TestBuildEngineDubUsesBedlessQueueForCallsOnly: a broadcast dub mixes each
// voice over a shared, delayed bed; a live call has no bed and no mixer — each
// target is a bounded VoiceQueue that speaks takes as they are synthesized.
func TestBuildEngineDubUsesBedlessQueueForCallsOnly(t *testing.T) {
	t.Parallel()

	registry := audio.NewRegistry(t.Context(), nil, nil, discard())
	deps := func() *laneDeps {
		return &laneDeps{
			synth: nopSynth{}, out: laneOutputs{audio: registry},
			voices: []core.Voice{{ID: "en", Lang: "en"}}, log: discard(),
		}
	}

	broadcast := &session.Session{Slug: "bcast", Profile: session.ProfileBroadcast, Langs: []core.Lang{"en"}}
	dub := buildEngineDub(broadcast, deps(), nil)
	if dub == nil || dub.Bed == nil {
		t.Fatalf("broadcast dub = %+v, want a shared bed", dub)
	}
	if _, ok := dub.Tracks["en"].(*pipeline.Track); !ok {
		t.Fatalf("broadcast target = %T, want a bed-mixed *pipeline.Track", dub.Tracks["en"])
	}

	call := &session.Session{Slug: "call", Profile: session.ProfileCall, Langs: []core.Lang{"en"}}
	dub = buildEngineDub(call, deps(), nil)
	if dub == nil || dub.Bed != nil {
		t.Fatalf("call dub = %+v, want no bed", dub)
	}
	if _, ok := dub.Tracks["en"].(*pipeline.VoiceQueue); !ok {
		t.Fatalf("call target = %T, want *pipeline.VoiceQueue", dub.Tracks["en"])
	}
}

// TestBuildEngineDubBindsOneVoicePerTarget: a bidirectional lane must voice
// each target with the voice configured for that language — the binding a
// two-way call depends on.
func TestBuildEngineDubBindsOneVoicePerTarget(t *testing.T) {
	t.Parallel()

	registry := audio.NewRegistry(t.Context(), nil, nil, discard())
	s := &session.Session{Slug: "twoway", Langs: []core.Lang{"it", "en-GB"}}
	deps := &laneDeps{
		synth: nopSynth{}, out: laneOutputs{audio: registry}, log: discard(),
		voices: []core.Voice{{ID: "lessac", Lang: "en"}, {ID: "paola", Lang: "it"}},
	}

	dub := buildEngineDub(s, deps, nil)
	if dub == nil || len(dub.Tracks) != 2 || len(dub.Voices) != 2 {
		t.Fatalf("dub = %+v, want two voiced targets", dub)
	}
	if dub.Voices["it"].ID != "paola" || dub.Voices["en-GB"].ID != "lessac" {
		t.Fatalf("per-target voices = %+v, want paola for it and lessac for en-GB", dub.Voices)
	}
}

// nopSynth is a synthesizer that yields no audio.
type nopSynth struct{}

func (nopSynth) Close() error { return nil }

func (nopSynth) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*engine.AudioStream, error) {
	out := make(chan core.PCM)
	close(out)
	result := make(chan error, 1)
	result <- nil
	close(result)

	return engine.NewAudioStream(out, result), nil
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func decodeStartupLogs(t *testing.T, raw []byte) []map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode startup log %q: %v", line, err)
		}
		entries = append(entries, entry)
	}

	return entries
}

func startupObserverForTest(
	output io.Writer, s *session.Session, step time.Duration,
) *laneStartupObserver {
	clock := &steppingClock{now: time.Unix(1_700_000_000, 0), step: step}

	return &laneStartupObserver{
		log: slog.New(slog.NewJSONHandler(output, nil)), session: s.Slug,
		profile: s.Profile, started: clock.now, now: clock.tick,
	}
}

func assertStartupPhases(t *testing.T, entries []map[string]any, want ...string) {
	t.Helper()

	if len(entries) != len(want) {
		t.Fatalf("startup logs = %v, want %d phases", entries, len(want))
	}
	for i, phase := range want {
		if got := entries[i]["phase"]; got != phase {
			t.Fatalf("startup phase[%d] = %v, want %q", i, got, phase)
		}
	}
}

func assertLogOmits(t *testing.T, log string, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(log, secret) {
			t.Fatalf("startup log exposes %q: %s", secret, log)
		}
	}
}

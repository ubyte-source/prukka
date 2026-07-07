package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	fileingress "github.com/ubyte-source/prukka/internal/media/ingest/file"
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
		translator: translator,
		synth:      synth,
		ingress:    failingIngress{err: errIngress},
		out: laneOutputs{
			vtt:   vtt.NewRegistry(),
			audio: audio.NewRegistry(t.Context(), nil, nil, log),
			hls:   hls.NewStore(t.TempDir(), log),
		},
		log: log, voice: core.Voice{ID: "voice", Lang: "it"},
	}

	err := runEngineLane(t.Context(), d, func() {})
	if !errors.Is(err, errIngress) {
		t.Fatalf("runEngineLane error = %v, want ingress failure", err)
	}
	if translator.closed != 1 || synth.closed != 1 {
		t.Fatalf("provider closes = translator:%d synth:%d, want one each", translator.closed, synth.closed)
	}
}

func TestIngressForKeepsLoopingWAVOnTheNativeReader(t *testing.T) {
	t.Parallel()

	ingress, err := ingressFor("file:///tmp/take.WAV?loop=true", slog.New(slog.DiscardHandler))
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
		{name: "same language", source: "en", targets: []core.Lang{"en-US"}},
		{name: "auto deferred", targets: []core.Lang{"de"}},
		{name: "missing reverse", source: "en", targets: []core.Lang{"it"}, wantErr: "en to it"},
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

// TestNewTranscriberRequiresInstalledEngine: transcription runs on the bundled
// local engine and fails clearly until it is installed.
func TestNewTranscriberRequiresInstalledEngine(t *testing.T) {
	t.Parallel()

	installed := config.Default()
	installed.Providers.Local.Bin = stubEnginePath
	if transcriber, err := newTranscriber(installed, discard()); err != nil || transcriber == nil {
		t.Fatalf("transcriber = (%v, %v), want a port", transcriber, err)
	}

	missing := config.Default()
	if _, err := newTranscriber(missing, discard()); err == nil {
		t.Fatal("transcriber without an installed engine must fail")
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

			if got := sttThreads(test.cpus, test.maxLanes); got != test.want {
				t.Fatalf("sttThreads(%d, %d) = %d, want %d", test.cpus, test.maxLanes, got, test.want)
			}
		})
	}
}

// TestNewTranslatorRequiresInstalledEngine: translation runs on the bundled
// local engine and fails clearly until it is installed.
func TestNewTranslatorRequiresInstalledEngine(t *testing.T) {
	t.Parallel()

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
// installed.
func TestNewSynthesizerSelectsVoiceStage(t *testing.T) {
	t.Parallel()

	subtitlesOnly := config.Default()
	subtitlesOnly.Providers.Voices = config.VoicesOff
	if synth, _, err := newSynthesizer(subtitlesOnly, discard()); err != nil || synth != nil {
		t.Fatalf("voices=off = (%v, %v), want no synthesizer", synth, err)
	}

	installed := config.Default()
	installed.Providers.Voices = config.VoicesLocal
	installed.Providers.Local.Bin = stubEnginePath
	installed.Providers.Local.TTS.Voice = "preset"
	synth, voice, err := newSynthesizer(installed, discard())
	if err != nil || synth == nil || voice.ID == "" || voice.Lang != "en" {
		t.Fatalf("synthesizer = (%v, %v, %v), want a port and preset voice", synth, voice, err)
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

	deps := &laneDeps{synth: nopSynth{}, out: laneOutputs{audio: registry}, voice: core.Voice{ID: "v"}}
	dub := buildEngineDub(s, deps, nil)
	if dub == nil || dub.Tracks["it"] == nil || dub.Bed == nil {
		t.Fatalf("dub = %v, want a bed and one track per language", dub)
	}

	mixed := &session.Session{Slug: "mixed", Langs: []core.Lang{"it", "en-GB"}}
	englishOnly := &laneDeps{
		synth: nopSynth{}, out: laneOutputs{audio: registry},
		voice: core.Voice{ID: "en", Lang: "en"}, log: discard(),
	}
	dub = buildEngineDub(mixed, englishOnly, nil)
	if dub == nil || dub.Tracks["en-GB"] == nil || dub.Tracks["it"] != nil {
		t.Fatalf("language-scoped dub tracks = %v, want only en-GB", dub)
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

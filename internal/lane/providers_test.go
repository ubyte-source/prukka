package lane

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/nativewire"
	"github.com/ubyte-source/prukka/internal/speech"
)

// stubEnginePath is a non-empty local engine path so installedSpeechEngine
// treats the bundled engine as present without a real binary on disk.
const stubEnginePath = "stub-engine"

// TestNewTranscriberRequiresInstalledEngine: the state dir is hermetic, so a
// managed bundle on the host cannot satisfy the fallback.
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

// TestInstalledSpeechEngineFallsBackToManagedBundle: with providers.local.bin
// unset, Bin is this binary and the managed bundle root is the engine root.
func TestInstalledSpeechEngineFallsBackToManagedBundle(t *testing.T) {
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
	local, bundleRoot, err := installedSpeechEngine(&cfg.Providers)
	if err != nil || local.Bin != self || bundleRoot != speech.BundleRoot(stateDir) {
		t.Fatalf("managed fallback = (%+v, %q, %v); want Bin=%q root=%q",
			local, bundleRoot, err, self, speech.BundleRoot(stateDir))
	}
	if cfg.Providers.Local.Bin != "" {
		t.Fatal("fallback mutated the shared config snapshot")
	}

	assertManagedStagesSpawnResolvedBinary(t, cfg, self)
}

// writeManagedNativeTools plants the compiled helpers speech.Resolve requires.
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
// resolved binary: a blank Bin surfaces only at spawn time as "exec: no command".
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
		{name: "large host fills the whisper ceiling", cpus: 16, maxLanes: 2, want: 8},
		{name: "per helper ceiling", cpus: 32, maxLanes: 2, want: 8},
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

	got, fastDecode := sttTuning(session.ProfileBroadcast)
	if got != (nativewire.STTTuning{}) || fastDecode {
		t.Fatalf("broadcast tuning = (%+v, %v), want helper defaults", got, fastDecode)
	}
	got, fastDecode = sttTuning(session.ProfileCall)
	if got.SilenceHang != 300*time.Millisecond || got.MaxWindow != 5*time.Second ||
		got.MinSpeech != 250*time.Millisecond || !fastDecode {
		t.Fatalf("call tuning = (%+v, %v)", got, fastDecode)
	}
}

func TestSTTModelIsProfileScoped(t *testing.T) {
	t.Parallel()

	models := config.LocalSTT{
		Model:     "models/stt/ggml-base.bin",
		CallModel: "models/stt/ggml-tiny-q5_1.bin",
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

// TestBuildLaneProvidersRollbackWithVoicesOffDoesNotPanic: a nil *native.TTS
// boxed without a guard defeats closeProvider's nil check.
func TestBuildLaneProvidersRollbackWithVoicesOffDoesNotPanic(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cfg := config.Default()
	cfg.Providers.Local.Bin = stubEnginePath
	cfg.Providers.Voices = config.VoicesOff

	pool := dispatch.New(1, 1)
	defer pool.Close()
	s := &session.Session{
		Slug:       "voices-off-rollback",
		Profile:    session.ProfileCall,
		Langs:      []core.Lang{"en"},
		SourceLang: "it",
	}

	if _, err := buildProviders(t.Context(), cfg, pool, s, nil, discard(), nil); err == nil {
		t.Fatal("warming against the stub engine binary must fail")
	}
}

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

// TestNewSynthesizerSelectsVoiceStage: voices=off ships subtitles only.
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

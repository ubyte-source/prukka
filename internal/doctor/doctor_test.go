package doctor_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/doctor"
)

const (
	engineManifestName  = "prukka-engine-manifest.json"
	validEngineManifest = `{
  "schema": "prukka.engine.bundle",
  "version": 2,
  "kind": "native"
}
`
)

// byName indexes checks for assertions.
func byName(checks []doctor.Check) map[string]doctor.Check {
	out := make(map[string]doctor.Check, len(checks))
	for _, c := range checks {
		out[c.Name] = c
	}

	return out
}

func TestRunProducesEveryProbe(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cfg := config.Default()
	checks := byName(doctor.Run(cfg))

	for _, name := range []string{"config", "speech-engine", "ffmpeg", "state-dir"} {
		if _, ok := checks[name]; !ok {
			t.Errorf("missing probe %q", name)
		}
	}
}

func TestSpeechEngineProbeExplainsUnsetBinary(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	check := byName(doctor.Run(config.Default()))["speech-engine"]
	if check.Status != doctor.StatusWarn || check.Detail == "" {
		t.Fatalf("unset speech engine check = %+v, want an actionable warning", check)
	}
}

func TestSpeechEngineProbeWarnsForManifestlessSingleBinary(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	bin := engineBinPath(dir, "single-binary-engine")
	writeExecutable(t, bin)

	cfg := config.Default()
	cfg.Providers.Local.Bin = bin
	check := byName(doctor.Run(cfg))["speech-engine"]
	if check.Status != doctor.StatusWarn {
		t.Fatalf("manifestless compatible engine = %+v, want warn", check)
	}
	if !strings.Contains(check.Detail, engineManifestName) ||
		!strings.Contains(check.Detail, "readiness") {
		t.Fatalf("manifestless compatible engine detail = %q, want undeclared readiness", check.Detail)
	}
}

func TestSpeechEngineProbeFailsForMissingConfiguredBinary(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cfg := config.Default()
	cfg.Providers.Local.Bin = filepath.Join(t.TempDir(), "missing-engine")
	check := byName(doctor.Run(cfg))["speech-engine"]
	if check.Status != doctor.StatusFail {
		t.Fatalf("missing configured engine = %+v, want fail", check)
	}
}

func TestSpeechEngineProbeRejectsInvalidBundleManifest(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "not object", content: `[]`},
		{name: "malformed", content: `{"schema":`},
		{
			name:    "unknown field",
			content: `{"schema":"prukka.engine.bundle","version":2,"kind":"native","extra":true}`,
		},
		{
			name:    "duplicate field",
			content: `{"schema":"prukka.engine.bundle","version":2,"kind":"native","kind":"native"}`,
		},
		{
			name:    "wrong schema",
			content: `{"schema":"other","version":2,"kind":"native"}`,
		},
		{
			name:    "wrong version",
			content: `{"schema":"prukka.engine.bundle","version":1,"kind":"native"}`,
		},
		{
			name:    "wrong kind",
			content: `{"schema":"prukka.engine.bundle","version":2,"kind":"single-binary"}`,
		},
		{
			name:    "multiple values",
			content: validEngineManifest + `{}`,
		},
		{name: "oversized", content: strings.Repeat(" ", 4097)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := engineBinPath(dir, "prukka")
			writeExecutable(t, bin)
			if err := os.WriteFile(
				filepath.Join(dir, engineManifestName),
				[]byte(test.content),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			cfg := config.Default()
			cfg.Providers.Local.Bin = bin
			check := byName(doctor.Run(cfg))["speech-engine"]
			if check.Status != doctor.StatusFail || !strings.Contains(check.Detail, "manifest") {
				t.Fatalf("invalid bundle manifest check = %+v, want manifest failure", check)
			}
		})
	}
}

func TestSpeechEngineProbeRejectsNonRegularBundleManifest(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	bin := engineBinPath(dir, "prukka")
	writeExecutable(t, bin)
	if err := os.Mkdir(filepath.Join(dir, engineManifestName), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers.Local.Bin = bin
	check := byName(doctor.Run(cfg))["speech-engine"]
	if check.Status != doctor.StatusFail || !strings.Contains(check.Detail, "regular file") {
		t.Fatalf("non-regular bundle manifest check = %+v, want failure", check)
	}
}

func TestSpeechEngineProbeChecksBundleModels(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	bin := engineBinPath(dir, "prukka-engine")
	writeExecutable(t, bin)
	writeEngineManifest(t, dir)

	cfg := config.Default()
	cfg.Providers.Local.Bin = bin
	missing := byName(doctor.Run(cfg))["speech-engine"]
	if missing.Status != doctor.StatusFail {
		t.Fatalf("engine without models = %+v, want fail", missing)
	}

	for _, path := range engineModelFiles(cfg) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withoutTools := byName(doctor.Run(cfg))["speech-engine"]
	if withoutTools.Status != doctor.StatusFail {
		t.Fatalf("engine without runtime tools = %+v, want fail", withoutTools)
	}
	for _, path := range engineExecutableFiles(cfg) {
		writeExecutable(t, filepath.Join(dir, path))
	}

	complete := byName(doctor.Run(cfg))["speech-engine"]
	assertStaticEngineWarning(t, complete, bin)

	// Every configured voice must be present: losing one voice of a
	// bidirectional bundle degrades a two-way call, so doctor names it.
	italianVoice := cfg.Providers.Local.TTS.Voices[1].Voice
	if err := os.Remove(filepath.Join(dir, italianVoice)); err != nil {
		t.Fatal(err)
	}
	oneVoiceGone := byName(doctor.Run(cfg))["speech-engine"]
	if oneVoiceGone.Status != doctor.StatusFail || !strings.Contains(oneVoiceGone.Detail, italianVoice) {
		t.Fatalf("engine with one voice missing = %+v, want fail naming %q", oneVoiceGone, italianVoice)
	}
}

func TestSpeechEngineProbeChecksOptionalCallModel(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers.Local.Bin = engineBinPath(dir, "prukka-engine")
	cfg.Providers.Local.STT.CallModel = "models/stt/ggml-tiny-q5_1.bin"
	writeExecutable(t, cfg.Providers.Local.Bin)
	writeEngineManifest(t, dir)
	writeEngineModels(t, dir, cfg)
	for _, path := range engineExecutableFiles(cfg) {
		writeExecutable(t, filepath.Join(dir, path))
	}

	assertStaticEngineWarning(t, byName(doctor.Run(cfg))["speech-engine"], cfg.Providers.Local.Bin)

	if err := os.Remove(filepath.Join(dir, cfg.Providers.Local.STT.CallModel)); err != nil {
		t.Fatal(err)
	}
	missing := byName(doctor.Run(cfg))["speech-engine"]
	if missing.Status != doctor.StatusFail ||
		!strings.Contains(missing.Detail, cfg.Providers.Local.STT.CallModel) {
		t.Fatalf("engine without call model = %+v, want failure naming the override", missing)
	}

	// An omitted override, or one equal to the primary model, deliberately uses
	// the already-checked primary artifact rather than creating another bundle
	// requirement.
	for _, callModel := range []string{"", cfg.Providers.Local.STT.Model} {
		cfg.Providers.Local.STT.CallModel = callModel
		assertStaticEngineWarning(t, byName(doctor.Run(cfg))["speech-engine"], cfg.Providers.Local.Bin)
	}
}

func TestSpeechEngineProbeChecksOnlyEnabledRuntimeTools(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	tests := []struct {
		name       string
		remove     string
		want       doctor.Status
		disableMT  bool
		disableTTS bool
	}{
		{name: "stt", remove: "whisper-server", want: doctor.StatusFail},
		{name: "mt", remove: "mt", want: doctor.StatusFail},
		{
			name:   "tts",
			remove: filepath.Join("piper", "piper"),
			want:   doctor.StatusFail,
		},
		{
			name:      "mt disabled",
			remove:    "mt",
			want:      doctor.StatusWarn,
			disableMT: true,
		},
		{
			name:       "tts disabled",
			remove:     filepath.Join("piper", "piper"),
			want:       doctor.StatusWarn,
			disableTTS: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Default()
			configureEngineStages(cfg, test.disableMT, test.disableTTS)
			cfg.Providers.Local.Bin = engineBinPath(dir, "prukka-engine")
			writeExecutable(t, cfg.Providers.Local.Bin)
			writeEngineManifest(t, dir)
			writeEngineModels(t, dir, cfg)
			for _, path := range engineExecutableFiles(cfg) {
				if path != test.remove {
					writeExecutable(t, filepath.Join(dir, path))
				}
			}

			check := byName(doctor.Run(cfg))["speech-engine"]
			if check.Status != test.want {
				t.Fatalf("engine check = %+v, want status %q", check, test.want)
			}
		})
	}
}

func configureEngineStages(cfg *config.Config, disableMT, disableTTS bool) {
	if disableMT {
		cfg.Providers.Local.MT.Pairs = nil
	}
	if disableTTS {
		cfg.Providers.Voices = config.VoicesOff
	}
}

func engineModelFiles(cfg *config.Config) []string {
	files := make(
		[]string,
		0,
		2+2*len(cfg.Providers.Local.TTS.Voices)+5*len(cfg.Providers.Local.MT.Pairs),
	)
	files = append(files, cfg.Providers.Local.STT.Model)
	callModel := cfg.Providers.Local.STT.CallModel
	if strings.TrimSpace(callModel) != "" && callModel != cfg.Providers.Local.STT.Model {
		files = append(files, callModel)
	}
	for _, voice := range cfg.Providers.Local.TTS.Voices {
		files = append(files, voice.Voice, voice.Voice+".json")
	}
	for _, pair := range cfg.Providers.Local.MT.Pairs {
		dir := filepath.Join("models", "mt-"+string(pair.From)+"-"+string(pair.To))
		for _, name := range []string{
			"config.json", "model.bin", "shared_vocabulary.json", "source.spm", "target.spm",
		} {
			files = append(files, filepath.Join(dir, name))
		}
	}

	return files
}

func engineExecutableFiles(cfg *config.Config) []string {
	files := []string{"whisper-server"}
	if len(cfg.Providers.Local.MT.Pairs) != 0 {
		files = append(files, "mt")
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		files = append(files, filepath.Join("piper", "piper"))
	}

	return files
}

func writeEngineModels(t *testing.T, dir string, cfg *config.Config) {
	t.Helper()

	for _, path := range engineModelFiles(cfg) {
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(dir, full)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeEngineManifest(t *testing.T, dir string) {
	t.Helper()

	if err := os.WriteFile(
		filepath.Join(dir, engineManifestName),
		[]byte(validEngineManifest),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func TestSpeechEngineProbeResolvesSymlinkedBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated privileges on Windows")
	}
	t.Setenv("PRUKKA_STATE", t.TempDir())

	bundle := t.TempDir()
	bin := engineBinPath(bundle, "prukka-engine")
	writeExecutable(t, bin)
	writeEngineManifest(t, bundle)

	cfg := config.Default()
	for _, path := range engineModelFiles(cfg) {
		full := filepath.Join(bundle, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range engineExecutableFiles(cfg) {
		writeExecutable(t, filepath.Join(bundle, path))
	}

	link := engineBinPath(t.TempDir(), "engine-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	cfg.Providers.Local.Bin = link

	check := byName(doctor.Run(cfg))["speech-engine"]
	assertStaticEngineWarning(t, check, bin)
}

func assertStaticEngineWarning(t *testing.T, check doctor.Check, bin string) {
	t.Helper()

	// The probe reports the fully resolved path (filepath.EvalSymlinks), which
	// on Windows also expands 8.3 short names to their long form; resolve the
	// fixture path the same way so the fragment check holds on every OS. The
	// probe reports the bundle directory quoted with %q, which escapes
	// backslashes on Windows, so compare against that same rendering of the
	// binary's parent — not the raw path.
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("resolve %q: %v", bin, err)
	}
	if check.Status != doctor.StatusWarn {
		t.Fatalf("static engine check = %+v, want warning", check)
	}
	for _, fragment := range []string{strconv.Quote(filepath.Dir(resolved)), "not tested"} {
		if !strings.Contains(check.Detail, fragment) {
			t.Fatalf("static engine detail %q lacks %q", check.Detail, fragment)
		}
	}
}

// engineBinPath builds the path to a fake engine binary inside dir. On Windows
// it appends the executable extension so exec.LookPath — which the probe uses to
// resolve providers.local.bin — can find it; other platforms need no suffix.
func engineBinPath(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	return filepath.Join(dir, name)
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("engine"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o100); err != nil {
		t.Fatal(err)
	}
}

func TestStateDirProbeOK(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if got := byName(doctor.Run(config.Default()))["state-dir"].Status; got != doctor.StatusOK {
		t.Fatalf("state-dir status = %q, want ok (writable temp dir)", got)
	}
}

func TestStateDirProbeDoesNotFollowPredictableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated privileges on Windows")
	}

	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixedProbe := filepath.Join(state, ".doctor-probe")
	if err := os.Symlink(victim, fixedProbe); err != nil {
		t.Fatal(err)
	}

	check := byName(doctor.Run(config.Default()))["state-dir"]
	if check.Status != doctor.StatusOK {
		t.Fatalf("state-dir check = %+v, want ok", check)
	}
	content, err := os.ReadFile(filepath.Clean(victim))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("predictable symlink target was modified: %q", content)
	}
	if info, err := os.Lstat(fixedProbe); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("predictable symlink was touched: info=%v err=%v", info, err)
	}
}

func TestStateDirProbeIsConcurrentAndLeavesNoFiles(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)
	cfg := config.Default()

	const workers = 32
	start := make(chan struct{})
	results := make(chan doctor.Check, workers)
	for range workers {
		go func() {
			<-start
			results <- byName(doctor.Run(cfg))["state-dir"]
		}()
	}
	close(start)

	for range workers {
		if check := <-results; check.Status != doctor.StatusOK {
			t.Errorf("concurrent state-dir check = %+v, want ok", check)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(state, ".doctor-probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("probe files left after concurrent checks: %v", leftovers)
	}
}

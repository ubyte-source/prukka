package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ubyte-source/prukka/internal/core/config"
)

// write drops a config file into a temp dir and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	return path
}

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Daemon.HTTP != "127.0.0.1:8080" {
		t.Fatalf("daemon.http = %q, want default", cfg.Daemon.HTTP)
	}

	if cfg.Defaults.Delay.Std() != 8*time.Second {
		t.Fatalf("defaults.delay = %v, want 8s", cfg.Defaults.Delay.Std())
	}
	if cfg.Providers.Voices != config.VoicesLocal {
		t.Fatalf("providers.voices = %q, want the local default", cfg.Providers.Voices)
	}
	if cfg.Providers.Local.TTS.Language != "en" {
		t.Fatalf("providers.local.tts.language = %q, want en", cfg.Providers.Local.TTS.Language)
	}
	pairs := cfg.Providers.Local.MT.Pairs
	if len(pairs) != 1 || pairs[0].From != "it" || pairs[0].To != "en" {
		t.Fatalf("providers.local.mt.pairs = %+v, want it to en", pairs)
	}
}

func TestLoadExplicitMissingFileFails(t *testing.T) {
	t.Parallel()

	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of explicit missing path succeeded, want error")
	}
}

func TestLoadDropsLegacyPrivacy(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, "privacy:\n  store_transcripts: 24h\n  store_audio: true\n"))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if strings.Contains(string(data), "privacy:") {
		t.Fatal("legacy privacy block survived normalization")
	}
}

func TestLoadMigratesRetiredFields(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, `
daemon:
  media: {rtmp: ":1935", srt: ":8890"}
control:
  remote: tls://example.test:7443
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := []string{"daemon.media", "control.remote"}
	if got := cfg.Deprecations(); !slices.Equal(got, want) {
		t.Fatalf("Deprecations = %v, want %v", got, want)
	}
}

func TestLoadMigratesRetiredLocalTuning(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, `
providers:
  local:
    base_url: http://localhost:8000
    mt: {model: old, temperature: 0.2}
    timeout: 120s
    stt: {base_url: http://localhost:8001, model: whisper-1}
    tts: {model: old, voice: alloy, voices: [old], format: pcm, rate: 16000}
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Deprecations()) != 7 {
		t.Fatalf("Deprecations = %v, want seven retired local groups", cfg.Deprecations())
	}
	if cfg.Providers.Local.STT.Model != "models/stt/ggml-base.bin" ||
		cfg.Providers.Local.TTS.Voice != "models/tts/en_US-lessac-medium.onnx" {
		t.Fatalf("legacy model ids were not migrated: %+v", cfg.Providers.Local)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	for _, retired := range []string{"base_url:", "temperature:", "timeout:", "format:", "rate:", "- old"} {
		if strings.Contains(string(data), retired) {
			t.Fatalf("retired field %q survived normalization:\n%s", retired, data)
		}
	}
}

func TestLoadFileOverridesAndNormalizes(t *testing.T) {
	t.Parallel()

	path := write(t, `
daemon:
  http: 127.0.0.1:9090
defaults:
  langs: [it, de-ch]
  delay: 4s
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Daemon.HTTP != "127.0.0.1:9090" {
		t.Fatalf("daemon.http = %q, want file value", cfg.Daemon.HTTP)
	}

	if got := cfg.Defaults.Langs[1]; got != "de-CH" {
		t.Fatalf("langs[1] = %q, want normalized de-CH", got)
	}
}

type invalidConfigCase struct {
	name string
	body string
	want string
}

func invalidCoreConfigCases() []invalidConfigCase {
	return []invalidConfigCase{
		{name: "unknown field", body: "daemon:\n  htpp: 1.2.3.4:80\n", want: "field htpp not found"},
		{name: "second document", body: "daemon: {}\n---\ndaemon: {}\n", want: "multiple YAML documents"},
		{name: "bad language", body: "defaults:\n  langs: [ch]\n", want: "did you mean"},
		{name: "empty default languages", body: "defaults:\n  langs: []\n", want: "at least one target language"},
		{name: "duplicate default language", body: "defaults:\n  langs: [it, IT]\n", want: "duplicate target language"},
		{name: "auto target language", body: "defaults:\n  langs: [auto]\n", want: "source language"},
		{name: "bad subs mode", body: "defaults:\n  subs: srt\n", want: "expected off, vtt or burn"},
		{name: "bad bed level", body: "defaults:\n  bed: loud\n", want: "expected off, or -60dB to 0dB"},
		{name: "bad duration", body: "defaults:\n  delay: fast\n", want: `duration "fast"`},
		{name: "excessive delay", body: "defaults:\n  delay: 61s\n", want: "expected 0s to 1m0s"},
		{name: "bad listen address", body: "daemon:\n  http: not-an-address\n", want: "daemon.http"},
		{name: "CORS path", body: "daemon:\n  cors_origin: https://example.test/ui\n", want: "without path"},
		{name: "empty STT model", body: "providers:\n  local:\n    stt:\n      model: ''\n", want: "local.stt.model"},
		{name: "empty local voice", body: "providers:\n  local:\n    tts:\n      voice: ''\n", want: "local.tts.voice"},
		{
			name: "bad local voice language", body: "providers:\n  local:\n    tts:\n      language: ch\n",
			want: "local.tts.language",
		},
		{
			name: "auto local voice language", body: "providers:\n  local:\n    tts:\n      language: auto\n",
			want: "concrete language",
		},
	}
}

func invalidProviderCases() []invalidConfigCase {
	return []invalidConfigCase{
		{
			name: "zero dispatch workers", body: "providers:\n  dispatch:\n    workers: 0\n",
			want: "providers.dispatch.workers",
		},
		{
			name: "oversized dispatch queue", body: "providers:\n  dispatch:\n    queue: 65537\n",
			want: "providers.dispatch.queue",
		},
		{
			name: "zero active lanes", body: "providers:\n  dispatch:\n    max_lanes: 0\n",
			want: "providers.dispatch.max_lanes",
		},
		{
			name: "fewer sessions than lanes",
			body: "providers:\n  dispatch:\n    max_lanes: 4\n    max_sessions: 3\n",
			want: "providers.dispatch.max_sessions",
		},
		{
			name: "unknown voices selector", body: "providers:\n  voices: cartesia\n",
			want: "providers.voices",
		},
		{
			name: "retired cloud field", body: "providers:\n  backend: local\n",
			want: "field backend not found",
		},
		{
			name: "unknown translation language",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: ch, to: en}]\n",
			want: "mt.pairs[0].from",
		},
		{
			name: "regional translation model",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: it-IT, to: en}]\n",
			want: "expected a base language",
		},
		{
			name: "duplicate translation pair",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: it, to: en}, {from: IT, to: EN}]\n",
			want: "duplicate it to en",
		},
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := slices.Concat(invalidCoreConfigCases(), invalidProviderCases())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestLoadAcceptsVoicesOff: subtitles-only is a valid voice-stage selection.
func TestLoadAcceptsVoicesOff(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, "providers:\n  voices: off\n"))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Providers.Voices != config.VoicesOff {
		t.Fatalf("voices = %q, want off", cfg.Providers.Voices)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("PRUKKA_HTTP", "127.0.0.1:7000")
	t.Setenv("PRUKKA_ENGINE_BIN", "/opt/prukka/engine")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Daemon.HTTP != "127.0.0.1:7000" {
		t.Fatalf("daemon.http = %q, want env value", cfg.Daemon.HTTP)
	}
	if cfg.Providers.Local.Bin != "/opt/prukka/engine" {
		t.Fatalf("providers.local.bin = %q, want environment override", cfg.Providers.Local.Bin)
	}
}

func TestStateDirHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRUKKA_STATE", dir)

	if got := config.StateDir(); got != dir {
		t.Fatalf("StateDir = %q, want %q", got, dir)
	}

	if got := config.TokenPath(); got != filepath.Join(dir, "control.token") {
		t.Fatalf("TokenPath = %q, want under override", got)
	}
}

func TestLaneFingerprintTracksOnlyLaneRelevantChanges(t *testing.T) {
	t.Parallel()

	base := config.Default()
	fp := base.LaneFingerprint()

	// Caption and delay defaults seed new sessions only: a live lane keeps
	// running, so the fingerprint must not move.
	unrelated := config.Default()
	unrelated.Defaults.Subs = "burn"
	unrelated.Defaults.Delay = config.Duration(3 * time.Second)
	unrelated.Providers.Dispatch.Workers = 3
	unrelated.Providers.Dispatch.Queue = 12
	unrelated.Providers.Dispatch.MaxLanes = 2
	unrelated.Providers.Dispatch.MaxSessions = 8
	if got := unrelated.LaneFingerprint(); got != fp {
		t.Fatalf("unrelated save changed the fingerprint:\n%s\n%s", fp, got)
	}

	// A model swap, the voice-stage selector and the bed level each rebuild
	// how lanes run, so each must move the fingerprint.
	for name, mutate := range map[string]func(*config.Config){
		"model":  func(c *config.Config) { c.Providers.Local.STT.Model = "models/stt/large.bin" },
		"bed":    func(c *config.Config) { c.Defaults.Bed = "off" },
		"voices": func(c *config.Config) { c.Providers.Voices = config.VoicesOff },
	} {
		changed := config.Default()
		mutate(changed)
		if changed.LaneFingerprint() == fp {
			t.Fatalf("%s change did not move the lane fingerprint", name)
		}
	}
}

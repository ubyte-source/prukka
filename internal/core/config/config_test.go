package config_test

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
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

// isolateDefaultConfig points the platform default config location at an
// empty temp dir: Load("") must exercise built-in defaults, never whatever
// config.yaml the daemon last persisted on the developer's machine.
func isolateDefaultConfig(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("HOME", dir)            // darwin, and the unix fallback
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("APPDATA", dir)         // windows
}

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	isolateDefaultConfig(t)

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
	assertBidirectionalDefaults(t, cfg)
}

// assertBidirectionalDefaults: the defaults must describe the bidirectional
// bundle — without both directions a default install cannot run a two-way call.
func assertBidirectionalDefaults(t *testing.T, cfg *config.Config) {
	t.Helper()

	voices := cfg.Providers.Local.TTS.Voices
	if len(voices) != 2 || voices[0].Language != "en" || voices[1].Language != "it" {
		t.Fatalf("providers.local.tts.voices = %+v, want en and it voices", voices)
	}
	pairs := cfg.Providers.Local.MT.Pairs
	if len(pairs) != 2 ||
		pairs[0] != (config.TranslationPair{From: "it", To: "en"}) ||
		pairs[1] != (config.TranslationPair{From: "en", To: "it"}) {
		t.Fatalf("providers.local.mt.pairs = %+v, want it<->en both ways", pairs)
	}
}

func TestDefaultSelectsQualityCallModel(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	if got := cfg.Providers.Local.STT.CallModel; got != "models/stt/ggml-base.bin" {
		t.Fatalf("default call model = %q, want bundled base model", got)
	}
}

func TestLoadPartialConfigRetainsQualityCallModel(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "daemon:\n  http: 127.0.0.1:9090\n"} {
		cfg, err := config.Load(write(t, body))
		if err != nil {
			t.Fatalf("Load(%q) returned error: %v", body, err)
		}
		if got := cfg.Providers.Local.STT.CallModel; got != "models/stt/ggml-base.bin" {
			t.Fatalf("Load(%q) call model = %q, want bundled base model", body, got)
		}
	}
}

func TestLoadExplicitMissingFileFails(t *testing.T) {
	t.Parallel()

	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of explicit missing path succeeded, want error")
	}
}

// TestLoadAcceptsMultipleVoices: a bidirectional bundle configures one voice
// per language, and the parsed set becomes the daemon's dubbing capability.
func TestLoadAcceptsMultipleVoices(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, `
providers:
  local:
    tts:
      voices:
        - {language: en, voice: models/tts/en_US-lessac-medium.onnx}
        - {language: it-it, voice: models/tts/it_IT-paola-medium.onnx}
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	voices := cfg.Providers.Local.TTS.Voices
	if len(voices) != 2 || voices[0].Language != "en" || voices[1].Language != "it-IT" {
		t.Fatalf("voices = %+v, want en and normalized it-IT", voices)
	}

	langs := cfg.Providers.Local.TTS.DubbedLanguages()
	if len(langs) != 2 || langs[0] != "en" || langs[1] != "it-IT" {
		t.Fatalf("DubbedLanguages = %v, want [en it-IT]", langs)
	}
}

func TestLoadAcceptsOptionalCallSTTModelAndFallsBack(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, `
providers:
  local:
    stt:
      model: models/stt/ggml-small.bin
      call_model: models/stt/ggml-tiny-q5_1.bin
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Providers.Local.STT.ModelForCall(); got != "models/stt/ggml-tiny-q5_1.bin" {
		t.Fatalf("ModelForCall = %q, want the configured call model", got)
	}

	fallback, err := config.Load(write(t, `
providers:
  local:
    stt:
      model: models/stt/ggml-small.bin
`))
	if err != nil {
		t.Fatalf("Load without call model returned error: %v", err)
	}
	if got := fallback.Providers.Local.STT.ModelForCall(); got != "models/stt/ggml-small.bin" {
		t.Fatalf("ModelForCall without override = %q, want the primary model", got)
	}

	nullFallback, err := config.Load(write(t, `
providers:
  local:
    stt:
      model: models/stt/ggml-small.bin
      call_model: null
`))
	if err != nil {
		t.Fatalf("Load with null call model returned error: %v", err)
	}
	if got := nullFallback.Providers.Local.STT.ModelForCall(); got != "models/stt/ggml-small.bin" {
		t.Fatalf("ModelForCall with null override = %q, want the primary model", got)
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
		{
			name: "blank call STT model",
			body: "providers:\n  local:\n    stt:\n      call_model: '   '\n",
			want: "local.stt.call_model",
		},
		{
			name: "no voices when local", body: "providers:\n  local:\n    tts:\n      voices: []\n",
			want: "at least one voice",
		},
		{
			name: "empty voice path",
			body: "providers:\n  local:\n    tts:\n      voices: [{language: en, voice: ''}]\n",
			want: "tts.voices[0].voice",
		},
		{
			name: "bad voice language",
			body: "providers:\n  local:\n    tts:\n      voices: [{language: ch, voice: v.onnx}]\n",
			want: "tts.voices[0].language",
		},
		{
			name: "auto voice language",
			body: "providers:\n  local:\n    tts:\n      voices: [{language: auto, voice: v.onnx}]\n",
			want: "concrete language",
		},
		{
			name: "duplicate voice language",
			body: `providers:
  local:
    tts:
      voices: [{language: en, voice: a.onnx}, {language: en-GB, voice: b.onnx}]
`,
			want: "duplicate voice language",
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
			name: "unknown translation target",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: en, to: ch}]\n",
			want: "mt.pairs[0].to",
		},
		{
			name: "regional translation target",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: en, to: it-IT}]\n",
			want: "mt.pairs[0].to",
		},
		{
			name: "automatic translation source",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: auto, to: en}]\n",
			want: "mt.pairs[0].from",
		},
		{
			name: "automatic translation target",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: en, to: auto}]\n",
			want: "mt.pairs[0].to",
		},
		{
			name: "duplicate translation pair",
			body: "providers:\n  local:\n    mt:\n      pairs: [{from: it, to: en}, {from: IT, to: EN}]\n",
			want: "duplicate it to en",
		},
	}
}

// unknownFieldConfigCases: spellings that name no configuration field fail
// the strict decoder exactly like any typo — the schema has one vocabulary.
func unknownFieldConfigCases() []invalidConfigCase {
	return []invalidConfigCase{
		{name: "privacy block", body: "privacy:\n  store_audio: true\n", want: "field privacy not found"},
		{name: "daemon media block", body: "daemon:\n  media: {rtmp: ':1935'}\n", want: "field media not found"},
		{
			name: "control remote",
			body: "control:\n  remote: tls://example.test:7443\n",
			want: "field remote not found",
		},
		{name: "provider backend", body: "providers:\n  backend: local\n", want: "field backend not found"},
		{
			name: "local base_url",
			body: "providers:\n  local:\n    base_url: http://localhost:8000\n",
			want: "field base_url not found",
		},
		{
			name: "local timeout",
			body: "providers:\n  local:\n    timeout: 120s\n",
			want: "field timeout not found",
		},
		{
			name: "stt base_url",
			body: "providers:\n  local:\n    stt:\n      base_url: http://localhost:8001\n",
			want: "field base_url not found",
		},
		{
			name: "mt model",
			body: "providers:\n  local:\n    mt:\n      model: old\n",
			want: "field model not found",
		},
		{
			name: "mt temperature",
			body: "providers:\n  local:\n    mt:\n      temperature: 0.2\n",
			want: "field temperature not found",
		},
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := slices.Concat(invalidCoreConfigCases(), invalidProviderCases(), unknownFieldConfigCases())
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
	isolateDefaultConfig(t)
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

	// Either STT model, the voice-stage selector and the bed level rebuild how
	// lanes run, so each must move the fingerprint.
	for name, mutate := range map[string]func(*config.Config){
		"model":      func(c *config.Config) { c.Providers.Local.STT.Model = "models/stt/large.bin" },
		"call model": func(c *config.Config) { c.Providers.Local.STT.CallModel = "models/stt/tiny.bin" },
		"bed":        func(c *config.Config) { c.Defaults.Bed = "off" },
		"voices":     func(c *config.Config) { c.Providers.Voices = config.VoicesOff },
	} {
		changed := config.Default()
		mutate(changed)
		if changed.LaneFingerprint() == fp {
			t.Fatalf("%s change did not move the lane fingerprint", name)
		}
	}
}

// Pack install/remove routes through these capability mutators; the invariants
// (dedup on add, one voice per language, clean removal) live here in config.

func TestLocalMTAddPairDedupesDirections(t *testing.T) {
	t.Parallel()

	var mt config.LocalMT
	mt.AddPair("it", "en")
	mt.AddPair("it", "en") // same direction: no duplicate
	mt.AddPair("en", "it") // reverse is a distinct model
	if len(mt.Pairs) != 2 {
		t.Fatalf("pairs = %v, want [it>en en>it] without duplicates", mt.Pairs)
	}

	mt.RemovePair("it", "en")
	if len(mt.Pairs) != 1 || mt.Pairs[0].From != "en" || mt.Pairs[0].To != "it" {
		t.Fatalf("after removal pairs = %v, want only [en>it]", mt.Pairs)
	}
}

func TestLocalTTSSetVoiceIsOnePerLanguage(t *testing.T) {
	t.Parallel()

	var tts config.LocalTTS
	tts.SetVoice("en", "en_US-amy")
	tts.SetVoice("it", "it_IT-riccardo")
	tts.SetVoice("en", "en_GB-alan") // replaces, does not append
	if len(tts.Voices) != 2 {
		t.Fatalf("voices = %v, want one per language", tts.Voices)
	}
	if got := tts.Voices[0]; got.Language != "en" || got.Voice != "en_GB-alan" {
		t.Fatalf("en voice = %+v, want the replacement en_GB-alan", got)
	}

	tts.RemoveVoice("en")
	if len(tts.Voices) != 1 || tts.Voices[0].Language != "it" {
		t.Fatalf("after removal voices = %v, want only it", tts.Voices)
	}
}

// FuzzLoad feeds arbitrary bytes through the strict loader: it must never
// panic, only ever return a valid *Config or an error — and a Config it
// accepts must hold every boundary invariant validate promises, because
// everything downstream trusts it without re-checking.
func FuzzLoad(f *testing.F) {
	f.Add("")
	f.Add("daemon:\n  http: 127.0.0.1:9090\n  cors_origin: https://prukka.ubyte.it\n")
	f.Add("defaults:\n  langs: [it, en]\n  delay: 8s\n  subs: vtt\n  bed: -15dB\n")
	f.Add("providers:\n  voices: off\n  local:\n    stt:\n      model: models/stt/x.bin\n")
	f.Add("providers:\n  dispatch:\n    workers: 1\n    queue: 1\n    max_lanes: 1\n    max_sessions: 1\n")
	f.Add("daemon: {http: \"[::1]:0\"}\n")
	f.Add("bogus: field\n")
	f.Add("---\na: 1\n---\nb: 2\n")
	f.Add("defaults:\n  delay: not-a-duration\n")
	f.Add("a: &a [*a]\n")
	f.Add("null\n")

	f.Fuzz(func(t *testing.T, body string) {
		cfg, err := config.Load(write(t, body))
		if err != nil {
			return
		}
		assertValidatedConfig(t, cfg)
	})
}

// assertValidatedConfig re-derives on an accepted Config what validate
// promises the rest of the daemon.
func assertValidatedConfig(t *testing.T, cfg *config.Config) {
	t.Helper()

	assertDaemonInvariants(t, cfg)
	assertDefaultsInvariants(t, cfg)
	assertProviderInvariants(t, cfg)
}

func assertDaemonInvariants(t *testing.T, cfg *config.Config) {
	t.Helper()

	if _, _, err := net.SplitHostPort(cfg.Daemon.HTTP); err != nil {
		t.Fatalf("accepted daemon.http %q does not split: %v", cfg.Daemon.HTTP, err)
	}
	if o := cfg.Daemon.CORSOrigin; o != "" && !cleanOrigin(o) {
		t.Fatalf("accepted daemon.cors_origin %q is not a bare http(s) origin", o)
	}
}

// cleanOrigin mirrors the promised origin shape: http(s), a host, and
// nothing else — no credentials, path, query or fragment.
func cleanOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}

	return u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func assertDefaultsInvariants(t *testing.T, cfg *config.Config) {
	t.Helper()

	assertDefaultLangs(t, cfg.Defaults.Langs)
	if _, err := session.ParseSubs(cfg.Defaults.Subs); err != nil {
		t.Fatalf("accepted defaults.subs %q does not parse: %v", cfg.Defaults.Subs, err)
	}
	if _, err := core.BedLevel(cfg.Defaults.Bed); err != nil {
		t.Fatalf("accepted defaults.bed %q does not parse: %v", cfg.Defaults.Bed, err)
	}
	if d := cfg.Defaults.Delay.Std(); d < 0 || d > core.MaxSessionDelay {
		t.Fatalf("accepted defaults.delay %v is outside [0, %v]", d, core.MaxSessionDelay)
	}
}

func assertDefaultLangs(t *testing.T, langs []core.Lang) {
	t.Helper()

	if len(langs) == 0 {
		t.Fatal("accepted config has no default target languages")
	}
	seen := make(map[core.Lang]bool, len(langs))
	for _, l := range langs {
		parsed, err := lang.Parse(string(l))
		if err != nil || parsed != l || parsed == core.LangAuto {
			t.Fatalf("accepted defaults.langs entry %q is not a normalized concrete language (%v)", l, err)
		}
		if seen[parsed] {
			t.Fatalf("accepted defaults.langs repeats %q", parsed)
		}
		seen[parsed] = true
	}
}

func assertProviderInvariants(t *testing.T, cfg *config.Config) {
	t.Helper()

	d := cfg.Providers.Dispatch
	if d.Workers < 1 || d.Queue < 1 || d.MaxLanes < 1 || d.MaxSessions < d.MaxLanes {
		t.Fatalf("accepted dispatch bounds are unusable: %+v", d)
	}
	if v := cfg.Providers.Voices; v != config.VoicesLocal && v != config.VoicesOff {
		t.Fatalf("accepted providers.voices %q is not a known selector", v)
	}
	if strings.TrimSpace(cfg.Providers.Local.STT.Model) == "" {
		t.Fatal("accepted config has no STT model")
	}
	assertConfiguredVoices(t, cfg)
	assertConfiguredPairs(t, cfg.Providers.Local.MT.Pairs)
}

func assertConfiguredVoices(t *testing.T, cfg *config.Config) {
	t.Helper()

	voices := cfg.Providers.Local.TTS.Voices
	if cfg.Providers.Voices == config.VoicesLocal && len(voices) == 0 {
		t.Fatal("accepted local-voices config lists no voices")
	}
	if cfg.Providers.Voices != config.VoicesLocal {
		return
	}
	seen := make(map[core.Lang]bool, len(voices))
	for i := range voices {
		parsed, err := lang.Parse(string(voices[i].Language))
		if err != nil || parsed == core.LangAuto || strings.TrimSpace(voices[i].Voice) == "" {
			t.Fatalf("accepted voice %d is malformed: %+v (%v)", i, voices[i], err)
		}
		if seen[parsed.Base()] {
			t.Fatalf("accepted voices repeat language %q", parsed.Base())
		}
		seen[parsed.Base()] = true
	}
}

// baseLang reports whether l is a normalized concrete base language — the
// only shape validate lets an MT pair carry.
func baseLang(l core.Lang) bool {
	parsed, err := lang.Parse(string(l))

	return err == nil && parsed == l && parsed != core.LangAuto && parsed.Base() == parsed
}

func assertConfiguredPairs(t *testing.T, pairs []config.TranslationPair) {
	t.Helper()

	seen := make(map[config.TranslationPair]bool, len(pairs))
	for i, pair := range pairs {
		if !baseLang(pair.From) || !baseLang(pair.To) {
			t.Fatalf("accepted mt pair %d is not base-to-base: %+v", i, pair)
		}
		if pair.From == pair.To || seen[pair] {
			t.Fatalf("accepted mt pair %d is degenerate or repeated: %+v", i, pair)
		}
		seen[pair] = true
	}
}

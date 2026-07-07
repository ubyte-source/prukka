package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	if cfg.Privacy.StoreAudio {
		t.Fatal("privacy.store_audio must default to false")
	}
}

func TestLoadExplicitMissingFileFails(t *testing.T) {
	t.Parallel()

	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of explicit missing path succeeded, want error")
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
budgets:
  per_session_eur_h: 1.5
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

	if cfg.Budgets.PerSessionEURPerHour != 1.5 {
		t.Fatalf("budget = %v, want 1.5", cfg.Budgets.PerSessionEURPerHour)
	}

	if cfg.Daemon.Media.RTMP != ":1935" {
		t.Fatalf("media.rtmp = %q, want untouched default", cfg.Daemon.Media.RTMP)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: "daemon:\n  htpp: 1.2.3.4:80\n", want: "field htpp not found"},
		{name: "bad language", body: "defaults:\n  langs: [ch]\n", want: "did you mean"},
		{name: "bad subs mode", body: "defaults:\n  subs: srt\n", want: "expected off, vtt or burn"},
		{name: "plain tcp remote", body: "control:\n  remote: tcp://0.0.0.0:7443\n", want: "only tls://"},
		{name: "bad duration", body: "defaults:\n  delay: fast\n", want: `duration "fast"`},
		{name: "bad listen address", body: "daemon:\n  http: not-an-address\n", want: "daemon.http"},
		{name: "bad backend", body: "providers:\n  backend: azure\n", want: "expected openrouter or local"},
		{name: "bad clone", body: "providers:\n  clone: elevenlabs\n", want: "expected off, pitch or cartesia"},
		{
			name: "bad local base_url",
			body: "providers:\n  backend: local\n  local:\n    base_url: not-a-url\n",
			want: "providers.local.base_url",
		},
		{
			name: "bad local stt override",
			body: "providers:\n  backend: local\n  local:\n    stt:\n      base_url: nope\n",
			want: "providers.local.stt.base_url",
		},
		{
			name: "bad cartesia base_url",
			body: "providers:\n  clone: cartesia\n  cartesia:\n    base_url: nope\n",
			want: "providers.cartesia.base_url",
		},
	}

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

// TestLoadAcceptsLocalAndClone: the on-machine backend with a cloud clone
// provider is a valid combination — each per-stage server has its own URL.
func TestLoadAcceptsLocalAndClone(t *testing.T) {
	t.Parallel()

	body := "providers:\n" +
		"  backend: local\n" +
		"  clone: cartesia\n" +
		"  local:\n" +
		"    stt:\n      base_url: http://127.0.0.1:8000/v1\n" +
		"    tts:\n      base_url: http://127.0.0.1:8880/v1\n"

	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Providers.Backend != config.BackendLocal || cfg.Providers.Clone != config.CloneCartesia {
		t.Fatalf("backend=%q clone=%q", cfg.Providers.Backend, cfg.Providers.Clone)
	}

	if cfg.Providers.Local.STT.BaseURL != "http://127.0.0.1:8000/v1" {
		t.Fatalf("local.stt.base_url = %q", cfg.Providers.Local.STT.BaseURL)
	}
}

// TestLoadAcceptsPitchAdaptation: the in-engine register-matching mode is a
// valid clone value on any backend, with nothing else to configure.
func TestLoadAcceptsPitchAdaptation(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(write(t, "providers:\n  clone: pitch\n"))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Providers.Clone != config.ClonePitch {
		t.Fatalf("clone = %q, want pitch", cfg.Providers.Clone)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("PRUKKA_HTTP", "127.0.0.1:7000")
	t.Setenv("PRUKKA_OPENROUTER_KEY", "keychain://prukka/alt")
	t.Setenv("PRUKKA_CARTESIA_KEY", "keychain://prukka/car-alt")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Daemon.HTTP != "127.0.0.1:7000" {
		t.Fatalf("daemon.http = %q, want env value", cfg.Daemon.HTTP)
	}

	if cfg.Providers.OpenRouter.Key != "keychain://prukka/alt" {
		t.Fatalf("openrouter.key = %q, want env value", cfg.Providers.OpenRouter.Key)
	}

	if cfg.Providers.Cartesia.Key != "keychain://prukka/car-alt" {
		t.Fatalf("cartesia.key = %q, want env value", cfg.Providers.Cartesia.Key)
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

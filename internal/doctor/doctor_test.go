package doctor_test

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/doctor"
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
	keyring.MockInit()
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cfg := config.Default()
	checks := byName(doctor.Run(cfg))

	for _, name := range []string{"ffmpeg", "keychain", "openrouter-key", "state-dir"} {
		if _, ok := checks[name]; !ok {
			t.Errorf("missing probe %q", name)
		}
	}

	// The default key reference is a keychain:// ref but the mock keyring is
	// empty, so it must warn rather than claim OK.
	if got := checks["openrouter-key"].Status; got != doctor.StatusWarn {
		t.Fatalf("openrouter-key status = %q, want warn (key not stored)", got)
	}
}

func TestProviderKeyProbeStates(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cfg := config.Default()

	// Plaintext key: warn to move it to the keychain.
	cfg.Providers.OpenRouter.Key = "sk-plaintext"
	if got := byName(doctor.Run(cfg))["openrouter-key"].Status; got != doctor.StatusWarn {
		t.Fatalf("plaintext key status = %q, want warn", got)
	}

	// Empty key: warn that AI stages stay offline.
	cfg.Providers.OpenRouter.Key = ""
	if got := byName(doctor.Run(cfg))["openrouter-key"].Status; got != doctor.StatusWarn {
		t.Fatalf("empty key status = %q, want warn", got)
	}

	// A resolvable keychain reference: OK.
	cfg.Providers.OpenRouter.Key = "keychain://prukka/openrouter"
	if err := keyring.Set("prukka", "openrouter", "sk-live"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}

	if got := byName(doctor.Run(cfg))["openrouter-key"].Status; got != doctor.StatusOK {
		t.Fatalf("resolvable key status = %q, want ok", got)
	}
}

func TestStateDirProbeOK(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if got := byName(doctor.Run(config.Default()))["state-dir"].Status; got != doctor.StatusOK {
		t.Fatalf("state-dir status = %q, want ok (writable temp dir)", got)
	}
}

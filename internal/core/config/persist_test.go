package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
)

// TestSaveLoadRoundTrip: a saved config loads back identical — the dashboard
// writes exactly what the daemon reads, durations in human form included.
func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	cfg := config.Default()
	cfg.Providers.Backend = config.BackendLocal
	cfg.Providers.Clone = config.ClonePitch
	cfg.Defaults.Langs = []core.Lang{"it", "en", "de"}
	cfg.Defaults.Delay = config.Duration(12 * time.Second)
	cfg.Budgets.PerSessionEURPerHour = 5.5

	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Providers.Backend != config.BackendLocal || loaded.Providers.Clone != config.ClonePitch {
		t.Fatalf("providers = %q/%q, want local/pitch", loaded.Providers.Backend, loaded.Providers.Clone)
	}

	if loaded.Defaults.Delay.Std() != 12*time.Second {
		t.Fatalf("delay = %v, want 12s", loaded.Defaults.Delay.Std())
	}

	if loaded.Budgets.PerSessionEURPerHour != 5.5 {
		t.Fatalf("budget = %v, want 5.5", loaded.Budgets.PerSessionEURPerHour)
	}

	if len(loaded.Defaults.Langs) != 3 || loaded.Defaults.Langs[2] != "de" {
		t.Fatalf("langs = %v, want [it en de]", loaded.Defaults.Langs)
	}
}

// TestSaveWritesHumanDurations: the file must say "8s", not nanoseconds —
// it stays hand-editable after the dashboard touched it.
func TestSaveWritesHumanDurations(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}

	if !strings.Contains(string(raw), "delay: 8s") {
		t.Fatalf("saved yaml lacks a human delay:\n%s", raw)
	}

	if strings.Contains(string(raw), "8000000000") {
		t.Fatal("saved yaml leaked raw nanoseconds")
	}
}

// TestSaveIsPrivateAndCreatesTheTree: the config may carry raw keys, so it
// must land 0600, in a directory created on demand, with no temp litter.
func TestSaveIsPrivateAndCreatesTheTree(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", "prukka")
	path := filepath.Join(dir, "config.yaml")

	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("saved config missing: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("config dir holds %d entries, want just the config (no temp litter)", len(entries))
	}
}

// TestSaveNamesTheEnvironmentOnFailure: an unwritable destination is an
// ErrPersist — the daemon's environment, never the caller's edit.
func TestSaveNamesTheEnvironmentOnFailure(t *testing.T) {
	t.Parallel()

	// The parent "directory" is a file: MkdirAll must fail.
	parent := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	err := config.Save(filepath.Join(parent, "config.yaml"), config.Default())
	if !errors.Is(err, config.ErrPersist) {
		t.Fatalf("Save error = %v, want ErrPersist", err)
	}
}

// TestKeyProvidersIsTheSharedRegistry: the CLI, the SetKey RPC and the
// dashboard all key off this one list.
func TestKeyProvidersIsTheSharedRegistry(t *testing.T) {
	t.Parallel()

	got := config.KeyProviders()
	if len(got) != 2 || got[0] != "openrouter" || got[1] != "cartesia" {
		t.Fatalf("KeyProviders = %v, want [openrouter cartesia]", got)
	}
}

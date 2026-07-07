package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/config"
)

func TestHolderReloadSwapsNonStructuralFields(t *testing.T) {
	t.Parallel()

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	if got := h.Current().Budgets.PerSessionEURPerHour; got != 1.0 {
		t.Fatalf("initial budget = %v, want 1.0", got)
	}

	rewrite(t, path, "budgets:\n  per_session_eur_h: 2.5\n")

	notes, reloadErr := h.Reload()
	if reloadErr != nil {
		t.Fatalf("Reload returned error: %v", reloadErr)
	}

	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none for a non-structural change", notes)
	}

	if got := h.Current().Budgets.PerSessionEURPerHour; got != 2.5 {
		t.Fatalf("budget after reload = %v, want 2.5", got)
	}
}

func TestHolderReportsStructuralChanges(t *testing.T) {
	t.Parallel()

	path := write(t, "daemon:\n  http: 127.0.0.1:9000\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	rewrite(t, path, "daemon:\n  http: 127.0.0.1:9001\n")

	notes, reloadErr := h.Reload()
	if reloadErr != nil {
		t.Fatalf("Reload returned error: %v", reloadErr)
	}

	if len(notes) != 1 || !strings.Contains(notes[0], "daemon.http") {
		t.Fatalf("notes = %v, want a daemon.http restart note", notes)
	}
}

func TestHolderKeepsOldConfigOnBadReload(t *testing.T) {
	t.Parallel()

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	rewrite(t, path, "defaults:\n  langs: [ch]\n")

	if _, reloadErr := h.Reload(); reloadErr == nil {
		t.Fatal("Reload accepted an invalid config")
	}

	if got := h.Current().Budgets.PerSessionEURPerHour; got != 1.0 {
		t.Fatalf("budget after failed reload = %v, want the previous 1.0", got)
	}
}

// rewrite replaces the config file content in place.
func rewrite(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
}

// TestHolderUpdatePersistsAndSwaps: a dashboard edit lands on disk, goes
// live immediately, and survives a fresh load — one transaction end to end.
func TestHolderUpdatePersistsAndSwaps(t *testing.T) {
	t.Parallel()

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	notes, err := h.Update(func(c *config.Config) {
		c.Providers.Clone = config.ClonePitch
		c.Budgets.PerSessionEURPerHour = 4.0
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none for non-structural edits", notes)
	}

	if got := h.Current(); got.Providers.Clone != config.ClonePitch || got.Budgets.PerSessionEURPerHour != 4.0 {
		t.Fatalf("live config = %q/%v, want pitch/4.0", got.Providers.Clone, got.Budgets.PerSessionEURPerHour)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after Update: %v", err)
	}

	if reloaded.Providers.Clone != config.ClonePitch {
		t.Fatalf("persisted clone = %q, want pitch", reloaded.Providers.Clone)
	}

	// The file edit must not have dropped the pre-existing file layer.
	if reloaded.Budgets.PerSessionEURPerHour != 4.0 {
		t.Fatalf("persisted budget = %v, want 4.0", reloaded.Budgets.PerSessionEURPerHour)
	}
}

// TestHolderUpdateRejectsInvalidEdits: a bad edit must reach neither disk
// nor the live snapshot — validation guards the transaction.
func TestHolderUpdateRejectsInvalidEdits(t *testing.T) {
	t.Parallel()

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	_, updateErr := h.Update(func(c *config.Config) {
		c.Providers.Clone = "elevenlabs"
	})
	if updateErr == nil || !strings.Contains(updateErr.Error(), "providers.clone") {
		t.Fatalf("Update error = %v, want a providers.clone validation failure", updateErr)
	}

	if got := h.Current().Providers.Clone; got != config.CloneOff {
		t.Fatalf("live clone after rejected edit = %q, want off", got)
	}

	reloaded, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatalf("Load after rejected Update: %v", loadErr)
	}

	if reloaded.Providers.Clone != config.CloneOff {
		t.Fatalf("disk clone after rejected edit = %q, want off", reloaded.Providers.Clone)
	}
}

// TestHolderUpdateDoesNotBakeEnvironment: an env override is live but must
// never be written to disk by an unrelated dashboard edit.
func TestHolderUpdateDoesNotBakeEnvironment(t *testing.T) {
	t.Setenv("PRUKKA_OPENROUTER_KEY", "env-only-key")

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	if _, updateErr := h.Update(func(c *config.Config) {
		c.Budgets.PerSessionEURPerHour = 2.0
	}); updateErr != nil {
		t.Fatalf("Update returned error: %v", updateErr)
	}

	// Live snapshot keeps the env layer…
	if got := h.Current().Providers.OpenRouter.Key; got != "env-only-key" {
		t.Fatalf("live key = %q, want the env override", got)
	}

	// …the file does not.
	raw, readErr := os.ReadFile(filepath.Clean(path))
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}

	if strings.Contains(string(raw), "env-only-key") {
		t.Fatal("environment override leaked into the config file")
	}
}

// TestHolderReloadSerializesWithUpdate: a racing reload must republish the
// edit's outcome, not resurrect the pre-edit file.
func TestHolderReloadSerializesWithUpdate(t *testing.T) {
	t.Parallel()

	path := write(t, "budgets:\n  per_session_eur_h: 1.0\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, updateErr := h.Update(func(c *config.Config) {
			close(entered)
			<-release // hold the transaction so the reload overlaps it
			c.Budgets.PerSessionEURPerHour = 4.0
		})
		if updateErr != nil {
			t.Errorf("Update returned error: %v", updateErr)
		}
	})

	<-entered

	reloadErr := make(chan error, 1)

	wg.Go(func() {
		_, e := h.Reload()
		reloadErr <- e
	})

	// Give the reload time to park on the writer lock, then let the
	// transaction finish under it.
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()

	if e := <-reloadErr; e != nil {
		t.Fatalf("Reload returned error: %v", e)
	}

	// A serialized reload re-reads the file the update saved; an
	// unserialized one could publish the stale 1.0 snapshot last.
	if got := h.Current().Budgets.PerSessionEURPerHour; got != 4.0 {
		t.Fatalf("budget after racing reload = %v, want the update's 4.0", got)
	}
}

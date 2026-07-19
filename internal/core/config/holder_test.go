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

	path := write(t, "defaults:\n  delay: 1s\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	if got := h.Current().Defaults.Delay.Std(); got != time.Second {
		t.Fatalf("initial delay = %v, want 1s", got)
	}

	rewrite(t, path, "defaults:\n  delay: 2s\n")

	notes, reloadErr := h.Reload()
	if reloadErr != nil {
		t.Fatalf("Reload returned error: %v", reloadErr)
	}

	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none for a non-structural change", notes)
	}

	if got := h.Current().Defaults.Delay.Std(); got != 2*time.Second {
		t.Fatalf("delay after reload = %v, want 2s", got)
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

func TestHolderReportsDispatchAsStructural(t *testing.T) {
	t.Parallel()

	path := write(t, "providers:\n  dispatch:\n    workers: 8\n    queue: 256\n")
	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	rewrite(t, path, "providers:\n  dispatch:\n    workers: 4\n    queue: 128\n")
	notes, reloadErr := h.Reload()
	if reloadErr != nil {
		t.Fatalf("Reload returned error: %v", reloadErr)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "providers.dispatch") {
		t.Fatalf("notes = %v, want a providers.dispatch restart note", notes)
	}
}

func TestHolderKeepsOldConfigOnBadReload(t *testing.T) {
	t.Parallel()

	path := write(t, "defaults:\n  delay: 1s\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	rewrite(t, path, "defaults:\n  langs: [ch]\n")

	if _, reloadErr := h.Reload(); reloadErr == nil {
		t.Fatal("Reload accepted an invalid config")
	}

	if got := h.Current().Defaults.Delay.Std(); got != time.Second {
		t.Fatalf("delay after failed reload = %v, want the previous 1s", got)
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

	path := write(t, "defaults:\n  delay: 1s\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	notes, err := h.Update(func(c *config.Config) {
		c.Providers.Voices = config.VoicesOff
		c.Defaults.Delay = config.Duration(4 * time.Second)
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none for non-structural edits", notes)
	}

	if got := h.Current(); got.Providers.Voices != config.VoicesOff || got.Defaults.Delay.Std() != 4*time.Second {
		t.Fatalf("live config = %q/%v, want off/4s", got.Providers.Voices, got.Defaults.Delay.Std())
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after Update: %v", err)
	}

	if reloaded.Providers.Voices != config.VoicesOff {
		t.Fatalf("persisted voices = %q, want off", reloaded.Providers.Voices)
	}

	// The file edit must not have dropped the pre-existing file layer.
	if reloaded.Defaults.Delay.Std() != 4*time.Second {
		t.Fatalf("persisted delay = %v, want 4s", reloaded.Defaults.Delay.Std())
	}
}

// TestHolderUpdateRejectsInvalidEdits: a bad edit must reach neither disk
// nor the live snapshot — validation guards the transaction.
func TestHolderUpdateRejectsInvalidEdits(t *testing.T) {
	t.Parallel()

	path := write(t, "defaults:\n  delay: 1s\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	_, updateErr := h.Update(func(c *config.Config) {
		c.Providers.Voices = "elevenlabs"
	})
	if updateErr == nil || !strings.Contains(updateErr.Error(), "providers.voices") {
		t.Fatalf("Update error = %v, want a providers.voices validation failure", updateErr)
	}

	if got := h.Current().Providers.Voices; got != config.VoicesLocal {
		t.Fatalf("live voices after rejected edit = %q, want local", got)
	}

	reloaded, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatalf("Load after rejected Update: %v", loadErr)
	}

	if reloaded.Providers.Voices != config.VoicesLocal {
		t.Fatalf("disk voices after rejected edit = %q, want local", reloaded.Providers.Voices)
	}
}

// TestHolderUpdateDoesNotBakeEnvironment: an env override is live but must
// never be written to disk by an unrelated dashboard edit.
func TestHolderUpdateDoesNotBakeEnvironment(t *testing.T) {
	t.Setenv("PRUKKA_HTTP", "127.0.0.1:7777")

	path := write(t, "defaults:\n  delay: 1s\n")

	h, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	if _, updateErr := h.Update(func(c *config.Config) {
		c.Defaults.Delay = config.Duration(2 * time.Second)
	}); updateErr != nil {
		t.Fatalf("Update returned error: %v", updateErr)
	}

	// Live snapshot keeps the env layer…
	if got := h.Current().Daemon.HTTP; got != "127.0.0.1:7777" {
		t.Fatalf("live http = %q, want the env override", got)
	}

	// …the file does not.
	raw, readErr := os.ReadFile(filepath.Clean(path))
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}

	if strings.Contains(string(raw), "127.0.0.1:7777") {
		t.Fatal("environment override leaked into the config file")
	}
}

// TestHolderReloadSerializesWithUpdate: a racing reload must republish the
// edit's outcome, not resurrect the pre-edit file.
func TestHolderReloadSerializesWithUpdate(t *testing.T) {
	t.Parallel()

	path := write(t, "defaults:\n  delay: 1s\n")

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
			c.Defaults.Delay = config.Duration(4 * time.Second)
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
	if got := h.Current().Defaults.Delay.Std(); got != 4*time.Second {
		t.Fatalf("delay after racing reload = %v, want the update's 4s", got)
	}
}

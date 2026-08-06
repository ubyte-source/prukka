//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceConfigAndSyncDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	destination := filepath.Join(dir, "config.yaml")
	staged := filepath.Join(dir, "staged.yaml")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatalf("seed staged file: %v", err)
	}

	if err := replaceConfig(staged, destination); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	if err := syncConfigDir(dir); err != nil {
		t.Fatalf("sync config directory: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	}()
	if got, err := root.ReadFile(filepath.Base(destination)); err != nil || string(got) != "new" {
		t.Fatalf("replacement = %q (%v), want new", got, err)
	}
}

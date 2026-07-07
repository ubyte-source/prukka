package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/config"
)

func TestDefaultPathIsAbsoluteAndNamesTheConfig(t *testing.T) {
	path := config.DefaultPath()

	if !filepath.IsAbs(path) {
		t.Fatalf("DefaultPath %q is not absolute", path)
	}

	if filepath.Base(path) != "config.yaml" {
		t.Fatalf("DefaultPath %q does not end in config.yaml", path)
	}
}

func TestStateDirHonorsTheOverride(t *testing.T) {
	t.Setenv("PRUKKA_STATE", "/tmp/prukka-test-state")

	if got := config.StateDir(); got != "/tmp/prukka-test-state" {
		t.Fatalf("StateDir with PRUKKA_STATE = %q", got)
	}
}

func TestTokenAndIPCPathsLiveInTheStateDir(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	state := config.StateDir()

	if !strings.HasPrefix(config.TokenPath(), state+string(filepath.Separator)) {
		t.Fatalf("TokenPath %q escapes the state dir %q", config.TokenPath(), state)
	}

	if config.IPCPath() == "" {
		t.Fatal("IPCPath is empty")
	}
}

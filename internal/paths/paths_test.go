package paths_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/paths"
)

func TestDefaultPathIsAbsoluteAndNamesTheConfig(t *testing.T) {
	path := paths.DefaultPath()

	if !filepath.IsAbs(path) {
		t.Fatalf("DefaultPath %q is not absolute", path)
	}

	if filepath.Base(path) != "config.yaml" {
		t.Fatalf("DefaultPath %q does not end in config.yaml", path)
	}
}

func TestDefaultPathIsUserWritableWithoutRoot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("system-wide default applies on Windows and as root")
	}

	t.Setenv("XDG_CONFIG_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	if path := paths.DefaultPath(); !strings.HasPrefix(path, home+string(filepath.Separator)) {
		t.Fatalf("DefaultPath %q is outside the home dir %q", path, home)
	}
}

func TestStateDirHonorsTheOverride(t *testing.T) {
	t.Setenv("PRUKKA_STATE", "/tmp/prukka-test-state")

	if got := paths.StateDir(); got != "/tmp/prukka-test-state" {
		t.Fatalf("StateDir with PRUKKA_STATE = %q", got)
	}
}

func TestTokenAndIPCPathsLiveInTheStateDir(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	state := paths.StateDir()

	if !strings.HasPrefix(paths.TokenPath(), state+string(filepath.Separator)) {
		t.Fatalf("TokenPath %q escapes the state dir %q", paths.TokenPath(), state)
	}

	// The basename is part of the contract: the control server mints the token
	// at exactly $STATE/control.token and out-of-process clients resolve it by
	// that name.
	if got := filepath.Base(paths.TokenPath()); got != "control.token" {
		t.Fatalf("TokenPath basename = %q, want control.token", got)
	}

	if paths.IPCPath() == "" {
		t.Fatal("IPCPath is empty")
	}
}

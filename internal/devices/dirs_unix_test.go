//go:build !windows

package devices

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestMkdirAllOpenDefeatsTheUmask: install runs under whatever umask the
// admin shell carries; the marker chain must come out world-traversable
// even under the most hostile one.
func TestMkdirAllOpenDefeatsTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	root := t.TempDir()
	dir := filepath.Join(root, "state", "devices")

	if err := mkdirAllOpen(dir); err != nil {
		t.Fatalf("mkdirAllOpen returned error: %v", err)
	}

	for _, d := range []string{filepath.Join(root, "state"), dir} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}

		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %o under umask 077, want 755", d, info.Mode().Perm())
		}
	}
}

// TestMkdirAllOpenRefusesALockedParent: a pre-existing root-only parent
// would hide every marker from unprivileged doctor runs — the write must
// refuse and name the exact repair instead of succeeding uselessly.
func TestMkdirAllOpenRefusesALockedParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "state")

	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("seed locked parent: %v", err)
	}

	err := mkdirAllOpen(filepath.Join(parent, "devices"))
	if err == nil {
		t.Fatal("a locked chain was accepted")
	}

	if !strings.Contains(err.Error(), "sudo chmod 755") {
		t.Fatalf("error %q does not name the repair command", err)
	}
}

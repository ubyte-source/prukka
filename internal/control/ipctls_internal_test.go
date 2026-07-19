package control

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The private key is the daemon's whole identity on the control channel, and
// its name in the state directory is predictable. Publishing through a rename
// replaces whatever stands at that name — a planted symlink included, which a
// write to the name would follow instead, handing the key to the link's target
// and keeping that file's own (possibly world-readable) mode.
func TestEnsureIPCKeypairDoesNotWriteThroughAPlantedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated privileges on Windows")
	}
	t.Parallel()

	state := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("stage victim: %v", err)
	}

	keyPath := filepath.Join(state, ipcKeyFile)
	if err := os.Symlink(victim, keyPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ensureIPCKeypair(state); err != nil {
		t.Fatalf("ensureIPCKeypair returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Clean(victim))
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("the private key was written through the symlink: %q", content)
	}

	// Lstat reads the name itself, so it answers both halves: the published key
	// is a regular file, and its mode came from the staged file rather than
	// from whatever the name carried before.
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("lstat published key: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the published key is still a symlink, want a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, want 0600", info.Mode().Perm())
	}
}

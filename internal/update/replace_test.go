package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplaceBinarySwapsAtomically(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "prukka")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed old binary: %v", err)
	}

	if err := replaceBinary(dest, []byte("new-image")); err != nil {
		t.Fatalf("replaceBinary returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Clean(dest))
	if err != nil || string(got) != "new-image" {
		t.Fatalf("dest = (%q, %v), want the new image", got, err)
	}

	// Windows decides executability by extension, not a mode bit.
	info, err := os.Stat(dest)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0) {
		t.Fatalf("dest mode = %v (%v), want owner-executable", info.Mode(), err)
	}

	if _, err := os.Stat(dest + ".new"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging file left behind: %v", err)
	}
	if _, err := os.Stat(dest + ".old"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("previous binary left behind: %v", err)
	}
}

func TestReplaceBinaryFailsWhenStagingCannotLand(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "prukka")
	if err := os.WriteFile(dest, []byte("known"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	// A directory squatting on the staging name defeats O_CREATE|O_WRONLY
	// everywhere; an unwritable-dir fixture is a no-op on Windows and
	// writable as root.
	if err := os.Mkdir(dest+".new", 0o750); err != nil {
		t.Fatalf("occupy staging name: %v", err)
	}

	if err := replaceBinary(dest, []byte("x")); err == nil {
		t.Fatal("staging over an occupied name succeeded")
	}

	got, err := os.ReadFile(filepath.Clean(dest))
	if err != nil || string(got) != "known" {
		t.Fatalf("dest = (%q, %v), want it untouched", got, err)
	}
	if _, err := os.Stat(dest + ".new"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging residue left behind: %v", err)
	}
}

package ffmpeg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// TestReplaceBinarySwapsAtomically: the new image lands executable at the
// destination and no staging file is left behind.
func TestReplaceBinarySwapsAtomically(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "prukka")
	if err := os.WriteFile(dest, []byte("old"), 0o700); err != nil {
		t.Fatalf("seed old binary: %v", err)
	}

	if err := ffmpeg.ReplaceBinary(dest, []byte("new-image")); err != nil {
		t.Fatalf("ReplaceBinary returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Clean(dest))
	if err != nil || string(got) != "new-image" {
		t.Fatalf("dest = (%q, %v), want the new image", got, err)
	}

	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("dest mode = %v (%v), want owner-executable", info.Mode(), err)
	}

	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Fatalf("staging file left behind: %v", err)
	}
}

// TestReplaceBinaryFailsIntoAnUnwritableDir: the destination is untouched
// when staging cannot happen.
func TestReplaceBinaryFailsIntoAnUnwritableDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := ffmpeg.ReplaceBinary(filepath.Join(dir, "prukka"), []byte("x")); err == nil {
		t.Fatal("staging into an unwritable dir succeeded")
	}
}

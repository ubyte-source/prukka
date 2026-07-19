//go:build !windows

package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveReplacedImage: retiring the previous image frees its name; on
// POSIX systems plain unlink already leaves the running process its inode.
func TestRemoveReplacedImage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "prukka.old")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if err := removeReplacedImage(path); err != nil {
		t.Fatalf("removeReplacedImage: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old image remains: %v", err)
	}
}

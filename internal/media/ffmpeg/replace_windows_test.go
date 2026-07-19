//go:build windows

package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveReplacedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prukka.old")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if err := removeReplacedImage(path); err != nil {
		t.Fatalf("removeReplacedImage: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old image remains: %v", err)
	}
}

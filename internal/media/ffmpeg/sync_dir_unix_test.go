//go:build darwin || linux

package ffmpeg

import (
	"path/filepath"
	"testing"
)

func TestSyncDirFlushesExistingDirectory(t *testing.T) {
	t.Parallel()

	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir: %v", err)
	}
}

func TestSyncDirReportsMissingDirectory(t *testing.T) {
	t.Parallel()

	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncDir succeeded on a missing directory")
	}
}

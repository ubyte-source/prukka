//go:build !darwin && !linux && !windows

package ffmpeg

import (
	"path/filepath"
	"testing"
)

func TestSyncDirIsANoOp(t *testing.T) {
	t.Parallel()

	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir: %v", err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("syncDir on a missing directory: %v", err)
	}
}

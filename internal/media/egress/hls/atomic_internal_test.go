package hls

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicReplacesContentWithoutScratchFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	if err := writeAtomic(path, []byte("new")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close test root: %v", closeErr)
		}
	})

	body, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(body) != "new" {
		t.Fatalf("destination = %q, want new", body)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("directory entries = %v, want only destination", entries)
	}
}

//go:build windows

package devices

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMkdirAllOpenCreatesTheChain: the marker directory chain comes up
// ready for unprivileged reads (ProgramData ACLs inherit).
func TestMkdirAllOpenCreatesTheChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "devices")

	if err := mkdirAllOpen(dir); err != nil {
		t.Fatalf("mkdirAllOpen returned error: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
}

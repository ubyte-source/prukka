//go:build windows

package speech

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The lock lives in the kernel, keyed by the open handle: a second handle must
// be refused while the first holds it and admitted the moment the holder closes,
// which is how a crashed holder is recovered from.
func TestLockFileExcludesSecondHandleUntilClose(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), lockName)
	holder, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	contender, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer closeQuietly(contender)

	if err := lockFile(holder); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := lockFile(contender); !errors.Is(err, ErrBusy) {
		t.Fatalf("contended lock = %v, want ErrBusy", err)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	if err := lockFile(contender); err != nil {
		t.Fatalf("lock after holder closed = %v, want immediate success", err)
	}
}

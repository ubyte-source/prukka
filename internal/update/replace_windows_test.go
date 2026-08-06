//go:build windows

package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// The old name must be free IMMEDIATELY: a DeleteFileW/os.Remove
// implementation leaves it delete-pending and fails the recreate below.
func TestRemoveReplacedImage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "prukka.old")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode path: %v", err)
	}
	if attrErr := windows.SetFileAttributes(pathPtr, windows.FILE_ATTRIBUTE_READONLY); attrErr != nil {
		t.Fatalf("mark image read-only: %v", attrErr)
	}
	// Model the running image: os.Open would not share DELETE and would turn
	// the removal into a sharing violation.
	handle, err := windows.CreateFile(pathPtr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("open running-image handle: %v", err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			t.Errorf("close running-image handle: %v", closeErr)
		}
	}()

	if err := removeReplacedImage(path); err != nil {
		t.Fatalf("removeReplacedImage: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old name still visible: %v", err)
	}
	// The freed name must be reusable while the old handle lives.
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("recreate at the freed name: %v", err)
	}
}

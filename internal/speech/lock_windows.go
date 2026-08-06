//go:build windows

package speech

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes the exclusive, non-blocking LockFileEx on the open file,
// reporting ErrBusy against a live holder; it is keyed by handle, so a second
// handle in the same process conflicts too.
func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrBusy
	}

	return err
}

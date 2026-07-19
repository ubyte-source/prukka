//go:build !windows

package speech

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes the exclusive, non-blocking kernel advisory lock on the open
// file, reporting ErrBusy against a live holder. flock is keyed by open file
// description, so a second descriptor in the same process conflicts too, and
// the kernel releases the lock when the holder's last descriptor closes —
// including on process death.
func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrBusy
	}

	return err
}

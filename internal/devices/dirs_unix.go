//go:build !windows

package devices

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// otherTraverse is the bit unprivileged doctor and status runs need on every
// directory above the markers.
const otherTraverse = 0o005

// withOpenUmask runs f with the process umask cleared, so extracted driver
// files keep the exact modes their archive records.
func withOpenUmask(f func() error) error {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	return f()
}

// mkdirAllOpen creates the marker directory chain world-traversable regardless
// of the caller's umask, and refuses a chain an earlier tool left locked.
func mkdirAllOpen(dir string) error {
	old := syscall.Umask(0)
	err := os.MkdirAll(dir, 0o755)

	syscall.Umask(old)

	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	for _, d := range []string{filepath.Dir(dir), dir} {
		info, statErr := os.Stat(d)
		if statErr != nil {
			return fmt.Errorf("inspect %s: %w", d, statErr)
		}

		if info.Mode().Perm()&otherTraverse == 0 {
			return fmt.Errorf(
				"%s is not world-traversable, so device markers would be unreadable without root — run: sudo chmod 755 %q",
				d, d)
		}
	}

	return nil
}

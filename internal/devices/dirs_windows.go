//go:build windows

package devices

import (
	"fmt"
	"os"
)

// mkdirAllOpen creates the marker directory chain. ProgramData inherits
// world-readable ACLs, so no POSIX mode juggling applies here.
func mkdirAllOpen(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	return nil
}

// withOpenUmask has no POSIX umask to clear on Windows.
func withOpenUmask(f func() error) error {
	return f()
}

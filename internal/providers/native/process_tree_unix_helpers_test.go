//go:build darwin || linux

package native

import (
	"errors"
	"syscall"
)

// descendantAlive reports whether pid still names a running process. Signal 0
// performs the permission and existence check without delivering anything.
func descendantAlive(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

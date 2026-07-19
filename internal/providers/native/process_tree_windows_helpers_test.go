//go:build windows

package native

import (
	"math"

	"golang.org/x/sys/windows"
)

// descendantAlive reports whether pid still names a running process: an
// unsignaled wait on its handle times out only while it is alive.
func descendantAlive(pid int) bool {
	if pid < 0 || int64(pid) > math.MaxUint32 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}

	status, err := windows.WaitForSingleObject(handle, 0)
	closeErr := windows.CloseHandle(handle)

	return err == nil && closeErr == nil && status == uint32(windows.WAIT_TIMEOUT)
}

//go:build windows

package native

import "golang.org/x/sys/windows"

func descendantAlive(pid int) bool {
	windowsPID, err := checkedWindowsPID(pid)
	if err != nil {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, windowsPID)
	if err != nil {
		return false
	}

	status, err := windows.WaitForSingleObject(handle, 0)
	closeErr := windows.CloseHandle(handle)

	return err == nil && closeErr == nil && status == uint32(windows.WAIT_TIMEOUT)
}

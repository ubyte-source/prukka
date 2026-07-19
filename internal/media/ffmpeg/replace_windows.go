//go:build windows

package ffmpeg

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileDispositionInfoEx struct {
	flags uint32
}

// removeReplacedImage applies Windows POSIX deletion semantics: the old
// name disappears now while the already-running process keeps its mapped
// image until exit.
func removeReplacedImage(path string) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode previous binary path: %w", err)
	}

	handle, err := windows.CreateFile(pathPtr, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("open previous binary for deletion: %w", err)
	}

	info := fileDispositionInfoEx{flags: windows.FILE_DISPOSITION_DELETE |
		windows.FILE_DISPOSITION_POSIX_SEMANTICS |
		windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE}
	setErr := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	closeErr := windows.CloseHandle(handle)
	if setErr != nil {
		return fmt.Errorf("mark previous binary for deletion: %w", setErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close previous binary deletion handle: %w", closeErr)
	}

	return nil
}

package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ReplaceBinary stages binary beside dest and renames it into place;
// Windows moves the running image aside first.
func ReplaceBinary(dest string, binary []byte) error {
	next := dest + ".new"

	staged, err := os.OpenFile(filepath.Clean(next), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	_, writeErr := staged.Write(binary)
	if err := errors.Join(writeErr, staged.Close()); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	if runtime.GOOS == osWindows {
		old := dest + ".old"
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear previous update: %w", err)
		}

		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("move running binary aside: %w", err)
		}
	}

	if err := os.Rename(next, dest); err != nil {
		return fmt.Errorf("activate new binary: %w", err)
	}

	return nil
}

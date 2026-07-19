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
func ReplaceBinary(dest string, binary []byte) (err error) {
	next := dest + ".new"
	activated := false
	defer func() {
		if activated {
			return
		}
		if removeErr := os.Remove(next); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove staged binary: %w", removeErr))
		}
	}()

	if stageErr := stageBinary(next, binary); stageErr != nil {
		return stageErr
	}

	if runtime.GOOS == osWindows {
		if activateErr := activateWindows(next, dest); activateErr != nil {
			return activateErr
		}
	} else if activateErr := os.Rename(next, dest); activateErr != nil {
		return fmt.Errorf("activate new binary: %w", activateErr)
	}

	activated = true

	return nil
}

func stageBinary(path string, binary []byte) error {
	staged, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	_, writeErr := staged.Write(binary)
	syncErr := staged.Sync()
	if err := errors.Join(writeErr, syncErr, staged.Close()); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	return nil
}

func activateWindows(next, dest string) error {
	old := dest + ".old"
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear previous update: %w", err)
	}
	if err := os.Rename(dest, old); err != nil {
		return fmt.Errorf("move running binary aside: %w", err)
	}
	if err := os.Rename(next, dest); err != nil {
		activationErr := fmt.Errorf("activate new binary: %w", err)
		if rollbackErr := os.Rename(old, dest); rollbackErr != nil {
			return errors.Join(activationErr, fmt.Errorf("restore previous binary: %w", rollbackErr))
		}

		return activationErr
	}
	if err := removeReplacedImage(old); err != nil {
		cleanupErr := os.Remove(dest)
		rollbackErr := os.Rename(old, dest)

		return errors.Join(fmt.Errorf("retire previous binary: %w", err), cleanupErr, rollbackErr)
	}

	return nil
}

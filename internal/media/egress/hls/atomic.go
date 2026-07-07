package hls

import (
	"errors"
	"os"
	"path/filepath"
)

func writeAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}

	tmpPath := tmp.Name()
	closed := false

	defer func() {
		if !closed {
			err = errors.Join(err, tmp.Close())
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		closed = true

		return err
	}
	closed = true

	return os.Rename(tmpPath, path)
}

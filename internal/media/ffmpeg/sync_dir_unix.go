//go:build darwin || linux

package ffmpeg

import (
	"errors"
	"os"
	"path/filepath"
)

func syncDir(path string) (err error) {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	return dir.Sync()
}

//go:build !windows

package config

import (
	"errors"
	"os"
)

func replaceConfig(staged, destination string) error {
	return os.Rename(staged, destination)
}

func syncConfigDir(path string) (err error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()

	return dir.Sync()
}

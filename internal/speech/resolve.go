package speech

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrNotInstalled reports that no managed engine bundle exists yet.
var ErrNotInstalled = errors.New("managed speech engine is not installed")

// Resolve returns the managed engine bundle root for one state directory,
// requiring a readable inventory and the compiled native helpers.
func Resolve(stateDir string) (string, error) {
	installer := &Installer{root: filepath.Join(stateDir, engineDirName)}
	if _, err := installer.State(); err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return "", err
		}

		return "", fmt.Errorf("managed speech engine inventory: %w", err)
	}

	root := BundleRoot(stateDir)
	if !nativeHelpersPresent(root) {
		return "", errors.New("managed speech engine is incomplete — run `prukka setup` to repair it")
	}

	return root, nil
}

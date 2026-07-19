package speech

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrNotInstalled reports that no managed engine bundle exists yet; callers
// surface their own remediation ("run prukka setup", a dashboard install
// button) on top of it.
var ErrNotInstalled = errors.New("managed speech engine is not installed")

// Resolve returns the managed engine bundle root for one state directory. It
// requires a readable inventory of the supported protocol and the compiled
// native helpers the daemon spawns; deeper layout validation stays with doctor.
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
		return "", fmt.Errorf("managed speech engine at %q is incomplete — run `prukka setup` to repair it", root)
	}

	return root, nil
}

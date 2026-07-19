// Package devices installs, removes and inspects the virtual devices
// (microphone, speaker, webcam) whose native drivers ship embedded in
// release builds.
package devices

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ubyte-source/prukka/internal/procio"
)

// ErrNotBundled reports a build compiled without embedded drivers.
var ErrNotBundled = errors.New("drivers are not bundled in this build — install a release build")

// Kind identifies one virtual device.
type Kind string

// The virtual devices prukka exposes to conferencing apps.
const (
	Microphone Kind = "microphone"
	Speaker    Kind = "speaker"
	Webcam     Kind = "webcam"
)

// State describes what happened to, or holds for, one device.
type State string

// Device states reported by Install, Remove and Status.
const (
	// StateInstalled: the driver is in place and current.
	StateInstalled State = "installed"
	// StateSkipped: already current, nothing was done.
	StateSkipped State = "skipped"
	// StateManual: the OS requires a user action, named in NextStep.
	StateManual State = "manual"
	// StateMissing: the driver is not installed.
	StateMissing State = "missing"
	// StateOutdated: an older driver is installed than this build embeds.
	StateOutdated State = "outdated"
	// StateRemoved: the driver was uninstalled.
	StateRemoved State = "removed"
)

// Result reports the outcome for one device.
type Result struct {
	// Kind is the device the result concerns.
	Kind Kind
	// State is what happened or holds.
	State State
	// NextStep names the single user action still needed, when any.
	NextStep string
}

// Install puts the embedded drivers in place, skipping devices that are
// already current and naming the one next step where the OS demands a
// user action.
func Install(ctx context.Context) ([]Result, error) {
	return install(ctx)
}

// Remove uninstalls the drivers and forgets their install markers.
func Remove(ctx context.Context) ([]Result, error) {
	return remove(ctx)
}

// Status reports each device against the bundled payloads and the
// recorded installs.
func Status(ctx context.Context) ([]Result, error) {
	return status(ctx)
}

// goosWindows names the Windows runtime.GOOS value across the package.
const goosWindows = "windows"

// executable names the running binary for user-facing commands: right
// after install PATH rarely carries it, and sudo resets PATH anyway.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "prukka"
	}

	return exe
}

// kinds lists every device in stable output order.
func kinds() []Kind {
	return []Kind{Microphone, Speaker, Webcam}
}

// installPayload stages one driver archive beside its destination and
// activates it with rollback, unless both the payload marker and path are
// already current.
func installPayload(kind Kind, data []byte, dir, name string) (state State, err error) {
	sum := payloadSum(data)
	dest := filepath.Join(dir, name)
	if payloadCurrent(kind, sum, dest) {
		return StateSkipped, nil
	}
	if filepath.Base(name) != name {
		return "", fmt.Errorf("invalid %s driver name %q", kind, name)
	}

	stage, staged, stageErr := stagePayload(kind, data, dir, name)
	if stageErr != nil {
		return "", stageErr
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()

	backup, previous, hadPrevious, backupErr := backupPayload(kind, dest, dir, name)
	if backupErr != nil {
		return "", backupErr
	}
	removeBackup := true
	defer func() {
		if removeBackup {
			err = errors.Join(err, os.RemoveAll(backup))
		}
	}()

	if activateErr := os.Rename(staged, dest); activateErr != nil {
		if restoreErr := restorePayload(dest, previous, hadPrevious, false); restoreErr != nil {
			removeBackup = false
			activateErr = errors.Join(activateErr, restoreErr)
		}

		return "", fmt.Errorf("activate %s driver: %w (%s)", kind, activateErr, privilegeHint)
	}

	if markerErr := writeMarker(kind, sum); markerErr != nil {
		rollbackErr := restorePayload(dest, previous, hadPrevious, true)
		if rollbackErr != nil {
			removeBackup = false
		}

		return "", errors.Join(markerErr, rollbackErr)
	}

	return StateInstalled, nil
}

func payloadCurrent(kind Kind, sum, dest string) bool {
	info, err := os.Lstat(dest)

	return recordedSum(kind) == sum && err == nil && info.IsDir()
}

func stagePayload(kind Kind, data []byte, dir, name string) (root, staged string, err error) {
	if mkErr := withOpenUmask(func() error { return os.MkdirAll(dir, 0o755) }); mkErr != nil {
		return "", "", fmt.Errorf("create %s driver directory: %w (%s)", kind, mkErr, privilegeHint)
	}

	root, err = os.MkdirTemp(dir, ".prukka-stage-")
	if err != nil {
		return "", "", fmt.Errorf("stage %s driver: %w (%s)", kind, err, privilegeHint)
	}
	if extractErr := extract(data, root); extractErr != nil {
		return "", "", errors.Join(
			fmt.Errorf("stage %s driver: %w (%s)", kind, extractErr, privilegeHint), os.RemoveAll(root))
	}

	staged = filepath.Join(root, name)
	if info, statErr := os.Lstat(staged); statErr != nil || !info.IsDir() {
		return "", "", errors.Join(
			fmt.Errorf("staged %s driver has no %s directory", kind, name), os.RemoveAll(root))
	}

	return root, staged, nil
}

func backupPayload(kind Kind, dest, dir, name string) (root, previous string, hadPrevious bool, err error) {
	root, err = os.MkdirTemp(dir, ".prukka-backup-")
	if err != nil {
		return "", "", false, fmt.Errorf("back up %s driver: %w (%s)", kind, err, privilegeHint)
	}
	previous = filepath.Join(root, name)

	if _, statErr := os.Lstat(dest); os.IsNotExist(statErr) {
		return root, previous, false, nil
	} else if statErr != nil {
		return "", "", false, errors.Join(
			fmt.Errorf("inspect previous %s driver: %w", kind, statErr), os.RemoveAll(root))
	}
	if moveErr := os.Rename(dest, previous); moveErr != nil {
		return "", "", false, errors.Join(
			fmt.Errorf("back up %s driver: %w (%s)", kind, moveErr, privilegeHint), os.RemoveAll(root))
	}

	return root, previous, true, nil
}

func restorePayload(dest, previous string, hadPrevious, dropActive bool) error {
	if dropActive {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("remove failed driver activation: %w", err)
		}
	}
	if !hadPrevious {
		return nil
	}
	if err := os.Rename(previous, dest); err != nil {
		return fmt.Errorf("previous driver remains at %s: %w", previous, err)
	}

	return nil
}

// removeAt deletes one installed driver path and its marker; a device
// that was never installed reports missing.
func removeAt(kind Kind, path string) (Result, error) {
	_, statErr := os.Stat(path)
	if recordedSum(kind) == "" && os.IsNotExist(statErr) {
		return Result{Kind: kind, State: StateMissing}, nil
	}

	if err := os.RemoveAll(path); err != nil {
		return Result{}, fmt.Errorf("remove %s driver: %w (%s)", kind, err, privilegeHint)
	}

	if err := dropMarker(kind); err != nil {
		return Result{}, err
	}

	return Result{Kind: kind, State: StateRemoved}, nil
}

// clearInstallRecords removes the dedicated staging and marker tree after
// every platform-owned device has been removed.
func clearInstallRecords() error {
	dir := devicesDir()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove device install records: %w", err)
	}
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) || (err == nil && len(entries) > 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect device install parent: %w", err)
	}
	if err := os.Remove(parent); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove empty device install parent: %w", err)
	}

	return nil
}

// runTool runs a system tool, folding its output into any error.
func runTool(ctx context.Context, name string, args ...string) error {
	return procio.RunQuiet(exec.CommandContext(ctx, name, args...))
}

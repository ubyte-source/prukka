package devices

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// devicesDir is the machine-wide home of the driver install markers and
// staged files. Drivers are system-wide, so their records are too —
// whoever runs the command; PRUKKA_STATE overrides it for tests.
func devicesDir() string {
	if v := os.Getenv("PRUKKA_STATE"); v != "" {
		return filepath.Join(v, "devices")
	}

	switch runtime.GOOS {
	case goosWindows:
		return filepath.Join(programDataRoot(), "devices")
	case "darwin":
		return "/Library/Application Support/Prukka/devices"
	default:
		return "/var/lib/prukka/devices"
	}
}

// programDataRoot is <ProgramData>\Prukka — the machine-wide home shared
// with the elevated webcam installer, which copies the DLL there. One
// resolver keeps the marker dir and the DLL path from drifting apart.
func programDataRoot() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return filepath.Join(v, "Prukka")
	}

	return `C:\ProgramData\Prukka`
}

// markerPath is where the recorded payload digest of a kind lives; its
// presence means "installed by prukka on this machine".
func markerPath(kind Kind) string {
	return filepath.Join(devicesDir(), string(kind)+".installed")
}

// payloadSum fingerprints a payload so a changed embed re-installs.
func payloadSum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// recordedSum reads the digest stored at install time; a device never
// installed reads as empty.
func recordedSum(kind Kind) string {
	out, err := os.ReadFile(filepath.Clean(markerPath(kind)))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// writeMarker records the installed payload digest for a kind; markers
// stay world-readable so an unprivileged update can spot stale drivers.
func writeMarker(kind Kind, sum string) error {
	path := markerPath(kind)
	if err := mkdirAllOpen(filepath.Dir(path)); err != nil {
		return err
	}

	if err := os.WriteFile(path, []byte(sum+"\n"), 0o644); err != nil {
		return fmt.Errorf("record %s install: %w", kind, err)
	}

	return nil
}

// dropMarker forgets a kind; dropping a never-installed kind succeeds.
func dropMarker(kind Kind) error {
	if err := os.Remove(markerPath(kind)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("forget %s install: %w", kind, err)
	}

	return nil
}

// markerState classifies a kind against the marker an install would
// record now; an empty want (unbundled build) reads a recorded install
// as installed.
func markerState(kind Kind, want string) State {
	recorded := recordedSum(kind)

	switch {
	case recorded == "":
		return StateMissing
	case want != "" && recorded != want:
		return StateOutdated
	default:
		return StateInstalled
	}
}

// expectedMarker is the marker value a payload would record; empty when
// the build carries no payload.
func expectedMarker(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	return payloadSum(data)
}

// Recorded reports whether any device driver was installed on this
// machine, so update knows to refresh them alongside the binary.
func Recorded() bool {
	for _, kind := range kinds() {
		if recordedSum(kind) != "" {
			return true
		}
	}

	return false
}

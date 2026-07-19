package devices

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallSurfacesErrNotBundled: a build without embedded drivers
// says so instead of guessing.
func TestInstallSurfacesErrNotBundled(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if _, err := Install(t.Context()); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("Install = %v, want ErrNotBundled", err)
	}
}

// TestStatusWorksUnbundled: status still reads the markers when the
// build embeds nothing, in stable device order.
func TestStatusWorksUnbundled(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	results, err := Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Status returned %d results, want 3", len(results))
	}

	for i, kind := range kinds() {
		if results[i].Kind != kind {
			t.Fatalf("results[%d].Kind = %q, want %q", i, results[i].Kind, kind)
		}
	}

	if results[2].Kind != Webcam || results[2].State != StateMissing {
		t.Fatalf("webcam = %+v, want missing", results[2])
	}
}

// TestInstallPayloadIsIdempotent: a second install with the same
// payload skips, a changed payload re-installs.
func TestInstallPayloadIsIdempotent(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()

	state, err := installPayload(Microphone, testArchive(t, "Bundle.driver", "one"), dir, "Bundle.driver")
	if err != nil || state != StateInstalled {
		t.Fatalf("first install = %v, %v", state, err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "Bundle.driver", "bin")); statErr != nil {
		t.Fatalf("extracted file missing: %v", statErr)
	}

	state, err = installPayload(Microphone, testArchive(t, "Bundle.driver", "one"), dir, "Bundle.driver")
	if err != nil || state != StateSkipped {
		t.Fatalf("repeat install = %v, %v, want skipped", state, err)
	}

	state, err = installPayload(Microphone, testArchive(t, "Bundle.driver", "two"), dir, "Bundle.driver")
	if err != nil || state != StateInstalled {
		t.Fatalf("changed install = %v, %v, want installed", state, err)
	}
}

func TestInstallPayloadRepairsACurrentMarkerWithMissingFiles(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	data := testArchive(t, "Bundle.driver", "driver")
	if _, err := installPayload(Microphone, data, dir, "Bundle.driver"); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if removeErr := os.RemoveAll(filepath.Join(dir, "Bundle.driver")); removeErr != nil {
		t.Fatalf("remove installed path: %v", removeErr)
	}

	state, err := installPayload(Microphone, data, dir, "Bundle.driver")
	if err != nil || state != StateInstalled {
		t.Fatalf("missing path with current marker = %v, %v, want reinstalled", state, err)
	}
}

func TestInstallPayloadRollsBackWhenTheMarkerCannotBeCommitted(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)

	dir := t.TempDir()
	if _, err := installPayload(Microphone, testArchive(t, "Bundle.driver", "old"), dir, "Bundle.driver"); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	blockedState := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedState, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("create blocked state path: %v", err)
	}
	t.Setenv("PRUKKA_STATE", blockedState)

	if _, err := installPayload(Microphone, testArchive(t, "Bundle.driver", "new"), dir, "Bundle.driver"); err == nil {
		t.Fatal("install succeeded despite an unwritable marker path")
	}
	got, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "Bundle.driver", "bin")))
	if err != nil || string(got) != "old" {
		t.Fatalf("driver after rollback = %q (%v), want old", got, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read driver directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".prukka-") {
			t.Errorf("staging residue survived: %s", entry.Name())
		}
	}
}

// TestRemoveAtDeletesDriverAndMarker: removal clears the disk and the
// record; a never-installed device reports missing.
func TestRemoveAtDeletesDriverAndMarker(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	dir := t.TempDir()
	path := filepath.Join(dir, "Camera.app")

	result, err := removeAt(Webcam, path)
	if err != nil || result.State != StateMissing {
		t.Fatalf("remove of absent driver = %+v, %v", result, err)
	}

	if _, installErr := installPayload(Webcam, testArchive(t, "Camera.app", "one"), dir, "Camera.app"); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}

	result, err = removeAt(Webcam, path)
	if err != nil || result.State != StateRemoved {
		t.Fatalf("remove = %+v, %v", result, err)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("driver survived removal: %v", statErr)
	}

	if recordedSum(Webcam) != "" {
		t.Fatal("marker survived removal")
	}
}

func TestClearInstallRecordsDropsOnlyTheDeviceTree(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)

	deviceFile := filepath.Join(devicesDir(), "src", "driver.c")
	if err := os.MkdirAll(filepath.Dir(deviceFile), 0o700); err != nil {
		t.Fatalf("create device tree: %v", err)
	}
	if err := os.WriteFile(deviceFile, []byte("driver"), 0o600); err != nil {
		t.Fatalf("write device file: %v", err)
	}
	keep := filepath.Join(state, "control.token")
	if err := os.WriteFile(keep, []byte("token"), 0o600); err != nil {
		t.Fatalf("write state sentinel: %v", err)
	}

	if err := clearInstallRecords(); err != nil {
		t.Fatalf("clearInstallRecords: %v", err)
	}
	if _, err := os.Stat(devicesDir()); !os.IsNotExist(err) {
		t.Fatalf("device tree survived: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated state was removed: %v", err)
	}
}

// TestRunToolFoldsTheInvocation: failures name the tool and arguments.
func TestRunToolFoldsTheInvocation(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("tool names differ on Windows")
	}

	err := runTool(t.Context(), "prukka-no-such-tool", "install")
	if err == nil || !strings.Contains(err.Error(), "prukka-no-such-tool install") {
		t.Fatalf("runTool error = %v, want the invocation named", err)
	}
}

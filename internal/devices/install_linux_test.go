//go:build linux

package devices

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStatusReadsTheMarkers(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if err := writeMarker(Microphone, payloadSum([]byte("src"))); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	results, err := status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	want := map[Kind]State{Microphone: StateInstalled, Speaker: StateMissing, Webcam: StateMissing}
	if len(results) != len(want) {
		t.Fatalf("status returned %d results, want %d", len(results), len(want))
	}

	for _, result := range results {
		if result.State != want[result.Kind] {
			t.Errorf("%s = %q, want %q", result.Kind, result.State, want[result.Kind])
		}
	}
}

func TestMissingToolchainNamesTheFix(t *testing.T) {
	t.Parallel()

	hint := missingToolchain("0.0.0-prukka-test")
	if !strings.Contains(hint, "linux-headers-0.0.0-prukka-test") {
		t.Fatalf("hint %q does not name the headers package", hint)
	}
}

// testKernel is a syntactically valid release the fixtures build paths from.
const testKernel = "6.8.0-prukka-test"

func plantModules(t *testing.T, root string, kinds ...Kind) {
	t.Helper()

	for _, kind := range kinds {
		path := moduleFile(root, testKernel, modules[kind].Name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("module"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestCurrentComparesAllMarkers(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	root := t.TempDir()
	sum := payloadSum([]byte("src"))
	if current(root, testKernel, sum) {
		t.Fatal("current before any install")
	}

	plantModules(t, root, kinds()...)
	for _, kind := range kinds() {
		if err := writeMarker(kind, sum); err != nil {
			t.Fatalf("writeMarker: %v", err)
		}
	}

	if !current(root, testKernel, sum) {
		t.Fatal("not current after all markers written")
	}

	if err := writeMarker(Webcam, payloadSum([]byte("older"))); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if current(root, testKernel, sum) {
		t.Fatal("current with a lagging webcam")
	}
}

func TestCurrentRequiresTheBuiltModules(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	root := t.TempDir()
	sum := payloadSum([]byte("src"))
	for _, kind := range kinds() {
		if err := writeMarker(kind, sum); err != nil {
			t.Fatalf("writeMarker: %v", err)
		}
	}

	if current(root, testKernel, sum) {
		t.Fatal("current with every marker written and no module file installed")
	}

	plantModules(t, root, kinds()...)
	if !current(root, testKernel, sum) {
		t.Fatal("not current with markers and modules in place")
	}

	if err := os.Remove(moduleFile(root, testKernel, modules[Webcam].Name)); err != nil {
		t.Fatalf("remove webcam module: %v", err)
	}

	if current(root, testKernel, sum) {
		t.Fatal("current with a wiped webcam module")
	}

	// A .ko surviving under another kernel is not this kernel's install.
	plantModules(t, filepath.Join(root, "elsewhere"), Webcam)
	if current(root, testKernel, sum) {
		t.Fatal("current with the webcam module only under another kernel tree")
	}
}

func TestStatusReportsAModuleWhoseFileIsGone(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	root := t.TempDir()
	want := linuxMarker([]byte("src"), testKernel)
	if err := writeMarker(Microphone, want); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}
	plantModules(t, root, Speaker)
	if err := writeMarker(Speaker, want); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	gone := moduleStatus(Microphone, want, root, testKernel)
	if gone.State != StateMissing || gone.NextStep != missingFilesNextStep {
		t.Fatalf("wiped microphone = %+v, want missing with the repair step", gone)
	}

	if live := moduleStatus(Speaker, want, root, testKernel); live.State != StateInstalled {
		t.Fatalf("speaker = %+v, want installed", live)
	}

	// An unbundled build knows no kernel, so it stays marker-only.
	if unbundled := moduleStatus(Microphone, "", root, ""); unbundled.State != StateInstalled {
		t.Fatalf("unbundled microphone = %+v, want the marker verdict", unbundled)
	}
}

func TestInstalledModuleFilesCoverEveryInstalledKernel(t *testing.T) {
	root := t.TempDir()

	want := []string{
		filepath.Join(root, "6.8.0", "extra", "prukka_webcam.ko"),
		filepath.Join(root, "6.9.0", "extra", "prukka_webcam.ko"),
	}
	for _, path := range want {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("module"), 0o600); err != nil {
			t.Fatalf("write module: %v", err)
		}
	}
	noise := filepath.Join(root, "6.9.0", "kernel", "prukka_webcam.ko")
	if err := os.MkdirAll(filepath.Dir(noise), 0o700); err != nil {
		t.Fatalf("mkdir noise: %v", err)
	}
	if err := os.WriteFile(noise, []byte("not owned"), 0o600); err != nil {
		t.Fatalf("write noise: %v", err)
	}

	got, err := installedModuleFiles(root, "prukka_webcam")
	if err != nil {
		t.Fatalf("installedModuleFiles: %v", err)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("installed module files = %v, want %v", got, want)
	}
	for i, path := range got {
		if kernel := moduleKernel(root, path); kernel != []string{"6.8.0", "6.9.0"}[i] {
			t.Errorf("moduleKernel(%q) = %q", path, kernel)
		}
	}
}

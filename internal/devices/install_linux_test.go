//go:build linux

package devices

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStatusReadsTheMarkers: each device reports its own record, in
// stable order.
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

// TestMissingToolchainNamesTheFix: absent kernel headers produce the
// package hint instead of a build failure.
func TestMissingToolchainNamesTheFix(t *testing.T) {
	t.Parallel()

	hint := missingToolchain("0.0.0-prukka-test")
	if !strings.Contains(hint, "linux-headers-0.0.0-prukka-test") {
		t.Fatalf("hint %q does not name the headers package", hint)
	}
}

// TestCurrentComparesAllMarkers: one lagging device makes the set stale.
func TestCurrentComparesAllMarkers(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	sum := payloadSum([]byte("src"))
	if current(sum) {
		t.Fatal("current before any install")
	}

	for _, kind := range kinds() {
		if err := writeMarker(kind, sum); err != nil {
			t.Fatalf("writeMarker: %v", err)
		}
	}

	if !current(sum) {
		t.Fatal("not current after all markers written")
	}

	if err := writeMarker(Webcam, payloadSum([]byte("older"))); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if current(sum) {
		t.Fatal("current with a lagging webcam")
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

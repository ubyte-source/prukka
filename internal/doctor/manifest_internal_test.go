package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDistributedEngineManifestMatchesDoctorContract(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "engine", engineManifestName)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeEngineManifest(data); err != nil {
		t.Fatalf("distributed engine manifest: %v", err)
	}
}

func TestCreateStateProbeIsUniqueAndPrivate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := createStateProbe(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createStateProbe(dir)
	if err != nil {
		if closeErr := first.Close(); closeErr != nil {
			t.Errorf("close first probe after second creation failure: %v", closeErr)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupStateProbes(t, first, second) })

	if first.Name() == second.Name() {
		t.Fatalf("temporary probes reused %q", first.Name())
	}
	if runtime.GOOS != windowsOS {
		for _, probe := range []*os.File{first, second} {
			assertPrivateStateProbe(t, probe)
		}
	}
}

func cleanupStateProbes(t *testing.T, probes ...*os.File) {
	t.Helper()

	for _, probe := range probes {
		if closeErr := probe.Close(); closeErr != nil {
			t.Errorf("close probe %q: %v", probe.Name(), closeErr)
		}
		if removeErr := os.Remove(probe.Name()); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			t.Errorf("remove probe %q: %v", probe.Name(), removeErr)
		}
	}
}

func assertPrivateStateProbe(t *testing.T, probe *os.File) {
	t.Helper()

	info, err := probe.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		t.Errorf("probe %q permissions = %04o, want no group/other access", probe.Name(), permissions)
	}
}

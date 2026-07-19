package speech

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveReportsNotInstalled(t *testing.T) {
	t.Parallel()

	if _, err := Resolve(t.TempDir()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

func TestResolveReturnsManagedBinary(t *testing.T) {
	t.Parallel()

	server := newArtifactServer(t)
	doc := catalogDoc(t, server, runtime.GOOS, runtime.GOARCH, runtimeArchive(t), map[string][]byte{
		"stt-core": packArchive(t, "models/stt/a.bin"),
	})
	catalog, err := ParseCatalog(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}

	stateDir := t.TempDir()
	installer := NewInstaller(stateDir, NewClient(server.server.URL), nil)
	if _, ensureErr := installer.EnsureRuntime(context.Background(), catalog); ensureErr != nil {
		t.Fatalf("ensure: %v", ensureErr)
	}

	root, err := Resolve(stateDir)
	if err != nil || root != BundleRoot(stateDir) {
		t.Fatalf("resolve: %q, %v", root, err)
	}
}

func TestResolveRejectsIncompleteInstall(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	root := filepath.Join(stateDir, engineDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeState(filepath.Join(root, stateName), sampleState()); err != nil {
		t.Fatalf("state: %v", err)
	}

	if _, err := Resolve(stateDir); err == nil || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("incomplete install must fail differently: %v", err)
	}
}

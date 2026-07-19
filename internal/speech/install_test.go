package speech

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// installFixture wires an installer against a loopback artifact server.
type installFixture struct {
	installer *Installer
	catalog   *Catalog
	server    *artifactServer
	stateDir  string
}

func newInstallFixture(t *testing.T, packs map[string][]byte) *installFixture {
	t.Helper()

	server := newArtifactServer(t)
	doc := catalogDoc(t, server, runtime.GOOS, runtime.GOARCH, runtimeArchive(t), packs)
	catalog, err := ParseCatalog(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("fixture catalog: %v", err)
	}

	stateDir := t.TempDir()

	return &installFixture{
		installer: NewInstaller(stateDir, NewClient(server.server.URL), nil),
		catalog:   catalog,
		server:    server,
		stateDir:  stateDir,
	}
}

func TestEnsureRuntimeInstallsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})

	changed, err := f.installer.EnsureRuntime(context.Background(), f.catalog)
	if err != nil || !changed {
		t.Fatalf("first ensure: changed=%v err=%v", changed, err)
	}
	if !nativeHelpersPresent(BundleRoot(f.stateDir)) {
		t.Fatal("runtime not published")
	}

	changed, err = f.installer.EnsureRuntime(context.Background(), f.catalog)
	if err != nil || changed {
		t.Fatalf("second ensure: changed=%v err=%v", changed, err)
	}
	if f.server.hits["/runtime.tar.gz"] != 1 {
		t.Fatalf("runtime downloaded %d times", f.server.hits["/runtime.tar.gz"])
	}
}

func TestInstallPackPublishesModelsAndState(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		"stt-core": packArchive(t, "models/stt/a.bin", "models/stt/b.bin"),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore); err != nil {
		t.Fatalf("install: %v", err)
	}
	bundleModel := filepath.Join(f.stateDir, engineDirName, bundleDirName, "models", "stt", "a.bin")
	if _, err := os.Stat(bundleModel); err != nil {
		t.Fatalf("model not published: %v", err)
	}

	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if f.server.hits["/stt-core.tar.gz"] != 1 {
		t.Fatalf("pack downloaded %d times", f.server.hits["/stt-core.tar.gz"])
	}

	// A deleted model file makes the no-op check fail and reinstalls.
	if err := os.Remove(bundleModel); err != nil {
		t.Fatalf("remove model: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore); err != nil {
		t.Fatalf("repair install: %v", err)
	}
	if f.server.hits["/stt-core.tar.gz"] != 2 {
		t.Fatalf("repair skipped the download: %d", f.server.hits["/stt-core.tar.gz"])
	}
}

func TestInstallPackRequiresRuntime(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	if err := f.installer.InstallPack(context.Background(), f.catalog, PackIDSTTCore); err == nil {
		t.Fatal("pack install without runtime must fail")
	}
}

func TestInstallPackRejectsFilesOutsideModels(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		"stt-core": tarGz(t, []tarEntry{{name: "prukka", body: []byte("evil"), mode: 0o755}}),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore); err == nil {
		t.Fatal("pack overwriting the runtime must fail")
	}
}

func TestRemovePackDeletesOwnedFiles(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		"stt-core": packArchive(t, "models/stt/a.bin"),
		"voice-it": packArchive(t, "models/tts/it_test-voice.onnx", "models/tts/it_test-voice.onnx.json"),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, id := range []string{PackIDSTTCore, "voice-it"} {
		if err := f.installer.InstallPack(ctx, f.catalog, id); err != nil {
			t.Fatalf("install %s: %v", id, err)
		}
	}

	if err := f.installer.RemovePack("voice-it"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	voicePath := filepath.Join(f.stateDir, engineDirName, bundleDirName, "models", "tts", "it_test-voice.onnx")
	if _, err := os.Stat(voicePath); err == nil {
		t.Fatal("voice file survived removal")
	}
	sttPath := filepath.Join(f.stateDir, engineDirName, bundleDirName, "models", "stt", "a.bin")
	if _, err := os.Stat(sttPath); err != nil {
		t.Fatalf("unrelated pack lost files: %v", err)
	}

	// Removal is idempotent.
	if err := f.installer.RemovePack("voice-it"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestEnsureRuntimeUpgradeCarriesModels(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore); err != nil {
		t.Fatalf("install: %v", err)
	}

	// A new runtime artifact (different bytes, same platform) upgrades the
	// bundle; the installed pack's models must survive.
	upgraded := tarGz(t, append([]tarEntry{
		{name: "prukka-engine-manifest.json", body: []byte(bundleManifestJSON), mode: 0o644},
		{name: "VERSION", body: []byte("v2"), mode: 0o644},
	}, nativeHelperEntries()...))
	rtURL, rtSHA, rtSize := f.server.add("/runtime2.tar.gz", upgraded)
	f.catalog.Runtimes[0] = Runtime{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: rtURL, SHA256: rtSHA, Size: rtSize}

	changed, err := f.installer.EnsureRuntime(ctx, f.catalog)
	if err != nil || !changed {
		t.Fatalf("upgrade: changed=%v err=%v", changed, err)
	}
	model := filepath.Join(f.stateDir, engineDirName, bundleDirName, "models", "stt", "a.bin")
	if _, statErr := os.Stat(model); statErr != nil {
		t.Fatalf("models lost in upgrade: %v", statErr)
	}
	state, err := f.installer.State()
	if err != nil || state.Runtime.SHA256 != rtSHA {
		t.Fatalf("state not upgraded: %+v, %v", state, err)
	}
	if _, ok := state.Pack(PackIDSTTCore); !ok {
		t.Fatal("pack record lost in upgrade")
	}
}

func TestOperationLockRejectsConcurrentWork(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	unlock, err := f.installer.lock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer unlock()

	if _, err := f.installer.EnsureRuntime(context.Background(), f.catalog); err == nil {
		t.Fatal("locked installer must refuse a second operation")
	}
}

func TestRecoverBundleRestoresInterruptedSwap(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Simulate a crash between the two swap renames: the bundle was retired
	// and no successor was published.
	root := filepath.Join(f.stateDir, engineDirName)
	if err := os.Rename(filepath.Join(root, bundleDirName), filepath.Join(root, bundleOldName)); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}

	f.installer.recoverBundle()
	if !nativeHelpersPresent(BundleRoot(f.stateDir)) {
		t.Fatal("interrupted swap not recovered")
	}
}

package speech

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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
		installer: NewInstaller(stateDir, NewClient(server.server.URL)),
		catalog:   catalog,
		server:    server,
		stateDir:  stateDir,
	}
}

func TestEnsureRuntimeInstallsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})

	changed, err := f.installer.EnsureRuntime(context.Background(), f.catalog, nil)
	if err != nil || !changed {
		t.Fatalf("first ensure: changed=%v err=%v", changed, err)
	}
	if !nativeHelpersPresent(BundleRoot(f.stateDir)) {
		t.Fatal("runtime not published")
	}

	changed, err = f.installer.EnsureRuntime(context.Background(), f.catalog, nil)
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
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	bundleModel := filepath.Join(f.stateDir, engineDirName, bundleDirName, "models", "stt", "a.bin")
	if _, err := os.Stat(bundleModel); err != nil {
		t.Fatalf("model not published: %v", err)
	}

	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if f.server.hits["/stt-core.tar.gz"] != 1 {
		t.Fatalf("pack downloaded %d times", f.server.hits["/stt-core.tar.gz"])
	}

	// A deleted model file makes the no-op check fail and reinstalls.
	if err := os.Remove(bundleModel); err != nil {
		t.Fatalf("remove model: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err != nil {
		t.Fatalf("repair install: %v", err)
	}
	if f.server.hits["/stt-core.tar.gz"] != 2 {
		t.Fatalf("repair skipped the download: %d", f.server.hits["/stt-core.tar.gz"])
	}
}

func TestInstallPackRequiresRuntime(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	if err := f.installer.InstallPack(context.Background(), f.catalog, PackIDSTTCore, nil); err == nil {
		t.Fatal("pack install without runtime must fail")
	}
}

func TestInstallPackRejectsFilesOutsideModels(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		"stt-core": tarGz(t, []tarEntry{{name: "prukka", body: []byte("evil"), mode: 0o755}}),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err == nil {
		t.Fatal("pack overwriting the runtime must fail")
	}
}

// Two ordinary catalog packs compose into a write outside the engine root.
// Every hop of the chain is lexically legal against its OWN parent, so the
// extractor's textual target check admits it; WalkDir lstats, so the links
// enter the pack's file list as plain "files"; and the kernel then resolves one
// directory further out per hop, so models/h3 is the state directory itself.
func TestInstallPackComposedSymlinkChainStaysInsideTheEngineRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		PackIDSTTCore: tarGz(t, []tarEntry{
			{name: modelsDirName, dir: true, mode: 0o755},
			{name: "models/h0", link: "."},
			{name: "models/h1", link: "h0/.."},
			{name: "models/h2", link: "h1/.."},
			{name: "models/h3", link: "h2/.."},
		}),
		"voice-it": packArchive(t, "models/h3/escaped.txt"),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	chainErr := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil)
	writeErr := f.installer.InstallPack(ctx, f.catalog, "voice-it", nil)

	escaped := filepath.Join(f.stateDir, "escaped.txt")
	if _, err := os.Lstat(escaped); err == nil {
		t.Fatalf("a pack wrote %s, outside the engine root %s", escaped, f.installer.root)
	}
	if chainErr == nil {
		t.Fatalf("a pack shipping symlinks must be rejected (second pack: %v)", writeErr)
	}
}

// The same chain, planted by the RUNTIME bundle: it legitimately ships symlinks
// (versioned dylib sonames) and is published by swapping the whole tree, so no
// per-file filter ever inspects it. Containment on the publish side can come
// only from the syscall layer.
func TestInstallPackRefusesToPublishThroughARuntimeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		PackIDSTTCore: packArchive(t, "models/h3/escaped.txt"),
	})
	hostile := tarGz(t, append([]tarEntry{
		{name: "prukka-engine-manifest.json", body: []byte(bundleManifestJSON), mode: 0o644},
		{name: modelsDirName, dir: true, mode: 0o755},
		{name: "models/h0", link: "."},
		{name: "models/h1", link: "h0/.."},
		{name: "models/h2", link: "h1/.."},
		{name: "models/h3", link: "h2/.."},
	}, nativeHelperEntries()...))
	rtURL, rtSHA, rtSize := f.server.add("/hostile-runtime.tar.gz", hostile)
	f.catalog.Runtimes[0] = Runtime{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: rtURL, SHA256: rtSHA, Size: rtSize}

	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil)
	escaped := filepath.Join(f.stateDir, "escaped.txt")
	if _, statErr := os.Lstat(escaped); statErr == nil {
		t.Fatalf("pack wrote %s, outside the engine root %s", escaped, f.installer.root)
	}
	if err == nil {
		t.Fatal("publishing through a bundle symlink must fail")
	}
}

// The model carry is the runtime publish's one per-file step, so a models/ that
// is itself a link — which only the runtime bundle can plant — must not be
// followed out of the engine root.
func TestCarryModelsRefusesASymlinkedModelsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// What an interrupted carry looks like when the successor bundle points its
	// models directory at the state directory, one level above the engine root.
	old := filepath.Join(f.installer.root, bundleOldName)
	if err := os.MkdirAll(filepath.Join(old, modelsDirName), 0o700); err != nil {
		t.Fatalf("stage retired bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, modelsDirName, "carried.bin"), []byte("model"), 0o600); err != nil {
		t.Fatalf("stage carried model: %v", err)
	}
	bundle := BundleRoot(f.stateDir)
	if err := os.RemoveAll(filepath.Join(bundle, modelsDirName)); err != nil {
		t.Fatalf("clear live models: %v", err)
	}
	for _, link := range [][2]string{{".", "a"}, {"a/..", "b"}, {"b/..", "c"}, {"c", modelsDirName}} {
		if err := os.Symlink(link[0], filepath.Join(bundle, link[1])); err != nil {
			t.Fatalf("plant %s: %v", link[1], err)
		}
	}

	f.installer.recoverBundle()

	escaped := filepath.Join(f.stateDir, "carried.bin")
	if _, err := os.Lstat(escaped); err == nil {
		t.Fatalf("the carry wrote %s, outside the engine root %s", escaped, f.installer.root)
	}
}

// A model pack carries data: a symlink, device node or socket has no
// legitimate place in it, even when the target is an innocent sibling.
func TestStagedModelFilesRejectsNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	stage := t.TempDir()
	models := filepath.Join(stage, modelsDirName)
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatalf("stage models: %v", err)
	}
	if err := os.WriteFile(filepath.Join(models, "a.bin"), []byte("model"), 0o600); err != nil {
		t.Fatalf("stage model: %v", err)
	}
	if err := os.Symlink("a.bin", filepath.Join(models, "alias.bin")); err != nil {
		t.Fatalf("plant alias: %v", err)
	}

	files, err := stagedModelFiles(stage)
	if err == nil {
		t.Fatalf("a symlink in a model pack must be rejected, got %v", files)
	}
}

func TestRemovePackDeletesOwnedFiles(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{
		"stt-core": packArchive(t, "models/stt/a.bin"),
		"voice-it": packArchive(t, "models/tts/it_test-voice.onnx", "models/tts/it_test-voice.onnx.json"),
	})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, id := range []string{PackIDSTTCore, "voice-it"} {
		if err := f.installer.InstallPack(ctx, f.catalog, id, nil); err != nil {
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

	// The prune takes the directory the pack emptied and stops there: the
	// models directory belongs to the bundle, not to any pack.
	models := filepath.Join(f.stateDir, engineDirName, bundleDirName, modelsDirName)
	if _, err := os.Stat(filepath.Join(models, "tts")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the emptied pack directory survived: %v", err)
	}
	if _, err := os.Stat(models); err != nil {
		t.Fatalf("the prune climbed past the models directory: %v", err)
	}

	// Removal is idempotent.
	if err := f.installer.RemovePack("voice-it"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

// Removal is the fourth thing that walks the bundle, and the bundle is the one
// artifact that legitimately ships symlinks: a successor runtime can keep
// models/ a real directory and plant the chain beneath it, which carryModels'
// Lstat skip lets through. Deleting along that chain must be refused by the
// same kernel handle the publish uses — both the unlink and the parent prune.
func TestRemovePackRefusesToDeleteThroughARuntimeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	plantEscapingChain(t, filepath.Join(BundleRoot(f.stateDir), modelsDirName))
	victim := filepath.Join(f.stateDir, "victim", "keep.bin")
	stageFile(t, victim)
	recordPackFiles(t, f.installer, PackIDSTTCore, "models/h3/victim/keep.bin")

	removeErr := f.installer.RemovePack(PackIDSTTCore)

	if _, err := os.Lstat(victim); err != nil {
		t.Fatalf("removal deleted %s, outside the engine root %s: %v", victim, f.installer.root, err)
	}
	if _, err := os.Lstat(filepath.Dir(victim)); err != nil {
		t.Fatalf("the prune deleted %s, outside the engine root %s: %v", filepath.Dir(victim), f.installer.root, err)
	}
	if removeErr == nil {
		t.Fatal("removing through a bundle symlink must fail")
	}
}

// plantEscapingChain plants the hop chain inside a models directory: every hop
// is lexically legal against its own parent, and the kernel resolves each one
// directory further out, so models/h3 is the state directory itself.
func plantEscapingChain(t *testing.T, models string) {
	t.Helper()

	for _, hop := range [][2]string{{".", "h0"}, {"h0/..", "h1"}, {"h1/..", "h2"}, {"h2/..", "h3"}} {
		if err := os.Symlink(hop[0], filepath.Join(models, hop[1])); err != nil {
			t.Fatalf("plant %s: %v", hop[1], err)
		}
	}
}

// stageFile writes one file, creating its parent directory.
func stageFile(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("stage %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatalf("stage %s: %v", path, err)
	}
}

// recordPackFiles rewrites one pack's file list in the live inventory: a
// runtime upgrade carries pack records forward onto a successor bundle, so the
// files a removal deletes are named by the state, not by the archive.
func recordPackFiles(t *testing.T, installer *Installer, id string, files ...string) {
	t.Helper()

	state, err := installer.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	state.upsertPack(&InstalledPack{ID: id, Kind: PackSTT, Files: files})
	if err := writeState(filepath.Join(installer.root, stateName), state); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func TestEnsureRuntimeUpgradeCarriesModels(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := f.installer.InstallPack(ctx, f.catalog, PackIDSTTCore, nil); err != nil {
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

	changed, err := f.installer.EnsureRuntime(ctx, f.catalog, nil)
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

	if _, err := f.installer.EnsureRuntime(context.Background(), f.catalog, nil); err == nil {
		t.Fatal("locked installer must refuse a second operation")
	}
}

// A crashed holder must not outlive its process: its lock rides an open file
// handle the kernel releases at death, so the next operation acquires
// immediately — the leftover lock FILE alone must never read as busy.
func TestOperationLockRecoversInstantlyFromCrashedHolder(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})

	// What a crash leaves behind: the lock file exists, freshly touched, with
	// no live process holding the kernel lock on it.
	if err := os.MkdirAll(f.installer.root, 0o700); err != nil {
		t.Fatalf("create engine root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.installer.root, lockName), nil, 0o600); err != nil {
		t.Fatalf("plant crashed holder's lock file: %v", err)
	}

	unlock, err := f.installer.lock()
	if err != nil {
		t.Fatalf("lock after crash = %v, want immediate acquisition", err)
	}
	unlock()

	// Release must be as immediate as recovery: no decay clock either way.
	unlock2, err := f.installer.lock()
	if err != nil {
		t.Fatalf("relock after unlock = %v, want immediate acquisition", err)
	}
	unlock2()
}

func TestRecoverBundleRestoresInterruptedSwap(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
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

// An interrupted model carry leaves both the retired and the published bundle
// present with some models still in old; recovery must re-home them, never
// lose them or leave old behind.
func TestRecoverBundleFinishesInterruptedModelCarry(t *testing.T) {
	t.Parallel()

	f := newInstallFixture(t, map[string][]byte{"stt-core": packArchive(t, "models/stt/a.bin")})
	ctx := context.Background()
	if _, err := f.installer.EnsureRuntime(ctx, f.catalog, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	root := filepath.Join(f.stateDir, engineDirName)
	bundle := filepath.Join(root, bundleDirName)
	old := filepath.Join(root, bundleOldName)

	// Simulate a crash mid-carry: a retired copy still holding a model the live
	// bundle does not yet have.
	if err := os.MkdirAll(filepath.Join(old, modelsDirName), 0o700); err != nil {
		t.Fatalf("stage old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, modelsDirName, "carried.bin"), []byte("x"), 0o600); err != nil {
		t.Fatalf("stage carried model: %v", err)
	}

	f.installer.recoverBundle()

	if _, err := os.Stat(filepath.Join(bundle, modelsDirName, "carried.bin")); err != nil {
		t.Fatalf("interrupted carry lost the model: %v", err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("retired bundle survived a completed carry: %v", err)
	}
}

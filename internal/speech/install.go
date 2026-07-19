package speech

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Managed layout under the daemon state directory:
//
//	engine/bundle/     the live bundle root the daemon points the self-executed
//	                   helpers at via PRUKKA_ENGINE_ROOT (native tools, libs,
//	                   models — no orchestrator binary; the daemon is the
//	                   orchestrator)
//	engine/state.json  the installed inventory
//	engine/.stage-*    per-operation staging, recovered or cleaned on entry
const (
	engineDirName = "engine"
	bundleDirName = "bundle"
	bundleOldName = "bundle.old"
	stagePrefix   = ".stage-"
	lockName      = ".operation.lock"
	modelsDirName = "models"
	staleStageAge = time.Hour
)

// ErrBusy reports a concurrent engine operation, either in this process or
// by another prukka process holding the operation lock.
var ErrBusy = errors.New("another engine operation is in progress")

// windowsOS mirrors the config package's platform constant.
const windowsOS = "windows"

// Installer performs engine install operations under one state directory.
type Installer struct {
	client   *Client
	progress Reporter
	root     string
}

// NewInstaller wires an installer; progress may be nil.
func NewInstaller(stateDir string, client *Client, progress Reporter) *Installer {
	return &Installer{client: client, root: filepath.Join(stateDir, engineDirName), progress: progress}
}

// BundleRoot is the managed engine bundle directory for one state directory:
// the root the daemon points PRUKKA_ENGINE_ROOT at when it self-executes the
// native speech helpers.
func BundleRoot(stateDir string) string {
	return filepath.Join(stateDir, engineDirName, bundleDirName)
}

// State reads the installed inventory; a missing install reports
// ErrNotInstalled.
func (i *Installer) State() (*State, error) {
	s, err := readState(filepath.Join(i.root, stateName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, err
	}

	return s, nil
}

// EnsureRuntime installs or upgrades the platform runtime; it reports
// whether anything changed. Installed model packs survive an upgrade.
func (i *Installer) EnsureRuntime(ctx context.Context, catalog *Catalog) (bool, error) {
	unlock, err := i.lock()
	if err != nil {
		return false, err
	}
	defer unlock()

	rt, err := catalog.RuntimeFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false, err
	}
	state, err := i.State()
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		return false, err
	}
	if state != nil && state.Runtime.SHA256 == rt.SHA256 && nativeHelpersPresent(filepath.Join(i.root, bundleDirName)) {
		return false, nil
	}
	if err := i.installRuntime(ctx, &rt); err != nil {
		return false, err
	}
	if err := i.recordRuntime(catalog.Protocol, &rt, state); err != nil {
		return false, err
	}
	i.report(Progress{Phase: PhaseDone, Item: "engine runtime"})

	return true, nil
}

// recordRuntime persists the post-install inventory, carrying pack records
// of the previous install forward.
func (i *Installer) recordRuntime(protocol int, rt *Runtime, previous *State) error {
	next := &State{Schema: stateSchema, Version: stateVersion, Protocol: protocol}
	next.Runtime = InstalledRun{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: rt.SHA256}
	if previous != nil {
		next.Packs = previous.Packs
	}

	return writeState(filepath.Join(i.root, stateName), next)
}

// installRuntime stages and publishes one runtime artifact.
func (i *Installer) installRuntime(ctx context.Context, rt *Runtime) error {
	stage, err := i.fetchToStage(ctx, "engine runtime", rt.URL, rt.SHA256, rt.Size)
	if err != nil {
		return err
	}
	defer removeTreeQuietly(stage)

	if !nativeHelpersPresent(stage) {
		return errors.New("engine runtime archive is missing its native speech tools")
	}

	return i.swapBundle(stage)
}

// InstallPack downloads and publishes one model pack into the bundle.
func (i *Installer) InstallPack(ctx context.Context, catalog *Catalog, id string) error {
	unlock, err := i.lock()
	if err != nil {
		return err
	}
	defer unlock()

	pack, err := catalog.PackByID(id)
	if err != nil {
		return err
	}
	state, err := i.State()
	if err != nil {
		return err
	}
	if installed, ok := state.Pack(id); ok && installed.SHA256 == pack.SHA256 && i.packFilesPresent(&installed) {
		return nil
	}

	files, err := i.stagePack(ctx, &pack)
	if err != nil {
		return err
	}

	state.upsertPack(&InstalledPack{
		ID: pack.ID, Kind: pack.Kind, From: pack.From, To: pack.To,
		Lang: pack.Lang, Voice: pack.Voice, SHA256: pack.SHA256, Files: files,
	})
	if err := writeState(filepath.Join(i.root, stateName), state); err != nil {
		return err
	}
	i.report(Progress{Phase: PhaseDone, Item: id})

	return nil
}

// stagePack downloads, verifies, extracts and publishes one pack's files,
// returning the bundle-relative paths it now owns.
func (i *Installer) stagePack(ctx context.Context, pack *Pack) ([]string, error) {
	stage, err := i.fetchToStage(ctx, pack.ID, pack.URL, pack.SHA256, pack.Size)
	if err != nil {
		return nil, err
	}
	defer removeTreeQuietly(stage)

	files, err := stagedModelFiles(stage)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", pack.ID, err)
	}
	if err := i.publishPackFiles(stage, files); err != nil {
		return nil, fmt.Errorf("pack %s: %w", pack.ID, err)
	}

	return files, nil
}

// RemovePack deletes one installed pack's files and record; removing a pack
// that is not installed is a no-op so removal is idempotent.
func (i *Installer) RemovePack(id string) error {
	unlock, err := i.lock()
	if err != nil {
		return err
	}
	defer unlock()

	state, err := i.State()
	if err != nil {
		return err
	}
	pack, ok := state.Pack(id)
	if !ok {
		return nil
	}

	bundle := filepath.Join(i.root, bundleDirName)
	for _, file := range pack.Files {
		path := filepath.Join(bundle, filepath.FromSlash(file))
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove pack %s: %w", id, err)
		}
		pruneEmptyParents(filepath.Dir(path), filepath.Join(bundle, modelsDirName))
	}

	state.dropPack(id)

	return writeState(filepath.Join(i.root, stateName), state)
}

// fetchToStage downloads, verifies and extracts one artifact into a fresh
// stage directory, returning its path.
func (i *Installer) fetchToStage(ctx context.Context, name, url, sha string, size int64) (string, error) {
	if err := os.MkdirAll(i.root, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", i.root, err)
	}
	i.cleanupStages()

	archive, err := os.CreateTemp(i.root, ".download-*")
	if err != nil {
		return "", fmt.Errorf("stage download: %w", err)
	}
	archivePath := archive.Name()
	defer removeQuietly(archivePath)

	if fetchErr := i.fetchArchive(ctx, archive, name, url, sha, size); fetchErr != nil {
		return "", fetchErr
	}

	i.report(Progress{Phase: PhaseInstall, Item: name})
	stage, err := os.MkdirTemp(i.root, stagePrefix)
	if err != nil {
		return "", fmt.Errorf("stage extract: %w", err)
	}
	reader, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		removeTreeQuietly(stage)

		return "", fmt.Errorf("reopen download: %w", err)
	}
	defer closeQuietly(reader)

	if _, err := extractArchive(reader, stage); err != nil {
		removeTreeQuietly(stage)

		return "", fmt.Errorf("extract %s: %w", name, err)
	}

	return stage, nil
}

func (i *Installer) fetchArchive(ctx context.Context, archive *os.File, name, url, sha string, size int64) error {
	if err := i.client.Fetch(ctx, name, url, sha, size, archive, i.progress); err != nil {
		closeQuietly(archive)

		return err
	}
	i.report(Progress{Phase: PhaseVerify, Item: name})
	if err := archive.Sync(); err != nil {
		closeQuietly(archive)

		return fmt.Errorf("sync download: %w", err)
	}

	return archive.Close()
}

// swapBundle publishes a staged runtime, carrying the previous bundle's model
// directories over so installed packs survive the upgrade. A crash between
// the two renames is repaired by recoverBundle on the next operation.
func (i *Installer) swapBundle(stage string) error {
	bundle := filepath.Join(i.root, bundleDirName)
	old := filepath.Join(i.root, bundleOldName)
	i.recoverBundle()

	if _, err := os.Stat(bundle); err == nil {
		if err := carryModels(bundle, stage); err != nil {
			return err
		}
		if err := os.Rename(bundle, old); err != nil {
			return fmt.Errorf("retire previous bundle: %w", err)
		}
	}
	if err := os.Rename(stage, bundle); err != nil {
		return fmt.Errorf("publish bundle: %w", err)
	}
	removeTreeQuietly(old)

	return nil
}

// recoverBundle finishes an interrupted swap: a retired bundle with no
// published successor is restored verbatim.
func (i *Installer) recoverBundle() {
	bundle := filepath.Join(i.root, bundleDirName)
	old := filepath.Join(i.root, bundleOldName)
	if _, err := os.Stat(bundle); errors.Is(err, fs.ErrNotExist) {
		if _, oldErr := os.Stat(old); oldErr == nil {
			if err := os.Rename(old, bundle); err != nil {
				return
			}
		}

		return
	}
	removeTreeQuietly(old)
}

// carryModels moves every model directory entry of the previous bundle into
// the staged one, keeping the staged runtime's own entries when both exist.
func carryModels(bundle, stage string) error {
	previous := filepath.Join(bundle, modelsDirName)
	entries, err := os.ReadDir(previous)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read previous models: %w", err)
	}

	for _, entry := range entries {
		dest := filepath.Join(stage, modelsDirName, entry.Name())
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(previous, entry.Name()), dest); err != nil {
			return fmt.Errorf("carry models/%s: %w", entry.Name(), err)
		}
	}

	return nil
}

// publishPackFiles moves staged pack files into the live bundle, replacing
// any previous copies.
func (i *Installer) publishPackFiles(stage string, files []string) error {
	bundle := filepath.Join(i.root, bundleDirName)
	for _, file := range files {
		relative := filepath.FromSlash(file)
		dest := filepath.Join(bundle, relative)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		removeQuietly(dest)
		if err := os.Rename(filepath.Join(stage, relative), dest); err != nil {
			return fmt.Errorf("publish %s: %w", file, err)
		}
	}

	return nil
}

// stagedModelFiles lists a staged pack's files and rejects anything outside
// the models tree: a pack must never touch runtime executables.
func stagedModelFiles(stage string) ([]string, error) {
	var files []string
	prefix := modelsDirName + "/"
	err := filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		relative, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(relative)
		if !strings.HasPrefix(slashed, prefix) {
			return fmt.Errorf("archive entry %q is outside %s/", slashed, modelsDirName)
		}
		files = append(files, slashed)

		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("archive carries no model files")
	}

	return files, nil
}

// packFilesPresent reports whether every file of an installed pack is still
// on disk, so a partially deleted pack reinstalls instead of no-oping.
func (i *Installer) packFilesPresent(pack *InstalledPack) bool {
	bundle := filepath.Join(i.root, bundleDirName)
	for _, file := range pack.Files {
		if _, err := os.Stat(filepath.Join(bundle, filepath.FromSlash(file))); err != nil {
			return false
		}
	}

	return true
}

// lock serializes engine operations across prukka processes: setup and the
// daemon must never stage over each other. A crashed holder's lock decays
// after staleStageAge.
func (i *Installer) lock() (func(), error) {
	if err := os.MkdirAll(i.root, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", i.root, err)
	}
	path := filepath.Join(i.root, lockName)
	for range 2 {
		f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			closeQuietly(f)

			return func() { removeQuietly(path) }, nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) < staleStageAge {
			return nil, ErrBusy
		}
		removeQuietly(path)
	}

	return nil, ErrBusy
}

// cleanupStages removes leftovers of interrupted operations that are old
// enough to be certainly abandoned.
func (i *Installer) cleanupStages() {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		stale := strings.HasPrefix(name, stagePrefix) || strings.HasPrefix(name, ".download-")
		if !stale {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < staleStageAge {
			continue
		}
		removeTreeQuietly(filepath.Join(i.root, name))
	}
}

func (i *Installer) report(p Progress) {
	if i.progress != nil {
		i.progress(p)
	}
}

// nativeHelpers are the compiled tools a runtime bundle must contain. The daemon
// self-executes its own engine subcommands, which spawn these helpers from the
// bundle root; the bundle no longer carries an orchestrator binary of its own.
func nativeHelpers() []string {
	suffix := ""
	if runtime.GOOS == windowsOS {
		suffix = ".exe"
	}

	helpers := []string{
		"whisper-server" + suffix,
		"mt" + suffix,
		filepath.Join("piper", "piper"+suffix),
	}
	if runtime.GOOS == "darwin" {
		// ffmpeg's raw AVFoundation input is silent under a launchd daemon;
		// the bundle ships the native capture helper (drivers/macos/capture)
		// so device sources work out of the box.
		helpers = append(helpers, "prukka-miccapture")
	}

	return helpers
}

// nativeHelpersPresent reports whether every compiled helper the engine spawns
// exists and is executable under dir.
func nativeHelpersPresent(dir string) bool {
	for _, helper := range nativeHelpers() {
		if !executableAt(filepath.Join(dir, helper)) {
			return false
		}
	}

	return true
}

// executableAt reports whether a regular, executable file exists at path.
func executableAt(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == windowsOS {
		return true
	}

	return info.Mode().Perm()&0o111 != 0
}

// removeTreeQuietly drops the error of a best-effort recursive cleanup.
func removeTreeQuietly(path string) {
	if path == "" {
		return
	}
	if err := os.RemoveAll(path); err != nil {
		return
	}
}

// pruneEmptyParents removes now-empty directories from child up to (and
// excluding) stop.
func pruneEmptyParents(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

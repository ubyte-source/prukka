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

	"github.com/ubyte-source/prukka/internal/enginebundle"
	"github.com/ubyte-source/prukka/internal/hostos"
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

// The deadlines both engine surfaces share: the daemon's engine RPCs
// (internal/control) and `prukka setup` bound the SAME operations with the
// SAME constants, so the two surfaces cannot drift apart.
const (
	// CatalogTimeout bounds one catalog fetch. It is short because the
	// catalog is a small document and callers block on the answer.
	CatalogTimeout = 8 * time.Second

	// OperationTimeout bounds one install operation — runtime or pack.
	// The largest artifact on a slow line still finishes well inside it.
	OperationTimeout = 45 * time.Minute
)

// Installer performs engine install operations under one state directory.
type Installer struct {
	client *Client
	root   string
}

// NewInstaller wires an installer. Progress reporting is per operation: the
// installer holds no sink, so one Installer serves callers with different
// destinations for the same download.
func NewInstaller(stateDir string, client *Client) *Installer {
	return &Installer{client: client, root: filepath.Join(stateDir, engineDirName)}
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
func (i *Installer) EnsureRuntime(
	ctx context.Context, catalog *Catalog, progress Reporter,
) (bool, error) {
	unlock, err := i.lock()
	if err != nil {
		return false, err
	}
	defer unlock()

	rt, err := catalog.RuntimeFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false, err
	}
	state, err := i.reusableState()
	if err != nil {
		return false, err
	}
	if state != nil && state.Runtime.SHA256 == rt.SHA256 && nativeHelpersPresent(filepath.Join(i.root, bundleDirName)) {
		return false, nil
	}
	if err := i.installRuntime(ctx, &rt, progress); err != nil {
		return false, err
	}
	if err := i.recordRuntime(catalog.Protocol, &rt, state); err != nil {
		return false, err
	}
	progress.report(Progress{Phase: PhaseDone, Item: "engine runtime"})

	return true, nil
}

// reusableState reads the inventory for a runtime upgrade: a not-installed or
// incompatible inventory reinstalls from scratch (nil state, no error); any
// other read error is fatal.
func (i *Installer) reusableState() (*State, error) {
	state, err := i.State()
	if err == nil || errors.Is(err, ErrNotInstalled) || errors.Is(err, errIncompatibleState) {
		return state, nil
	}

	return nil, err
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
func (i *Installer) installRuntime(ctx context.Context, rt *Runtime, progress Reporter) error {
	stage, err := i.fetchToStage(ctx, "engine runtime", rt.URL, rt.SHA256, rt.Size, progress)
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
func (i *Installer) InstallPack(
	ctx context.Context, catalog *Catalog, id string, progress Reporter,
) error {
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

	files, err := i.stagePack(ctx, &pack, progress)
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
	progress.report(Progress{Phase: PhaseDone, Item: id})

	return nil
}

// stagePack downloads, verifies, extracts and publishes one pack's files,
// returning the bundle-relative paths it now owns.
func (i *Installer) stagePack(ctx context.Context, pack *Pack, progress Reporter) ([]string, error) {
	stage, err := i.fetchToStage(ctx, pack.ID, pack.URL, pack.SHA256, pack.Size, progress)
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

// RemovePack deletes one installed pack's files and record; a pack that is not
// installed, or whose files are already gone, is a no-op so removal is
// idempotent. A name the kernel will not resolve inside the engine root fails
// the whole removal instead: the record stays, so the inventory keeps naming
// exactly what the bundle still holds.
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
	if err := i.removePackFiles(&pack); err != nil {
		return fmt.Errorf("remove pack %s: %w", id, err)
	}

	state.dropPack(id)

	return writeState(filepath.Join(i.root, stateName), state)
}

// removePackFiles unlinks one pack's files, and the directories they leave
// empty, through the very *os.Root that published them: deleting walks the
// same attacker-shaped tree as publishing, in the same direction of trust, so
// it carries the same containment — a component the kernel resolves outside
// the engine root is refused instead of followed.
func (i *Installer) removePackFiles(pack *InstalledPack) error {
	root, err := os.OpenRoot(i.root)
	if err != nil {
		return fmt.Errorf("open engine root: %w", err)
	}
	defer closeQuietly(root)

	models := filepath.Join(bundleDirName, modelsDirName)
	for _, file := range pack.Files {
		name := filepath.Join(bundleDirName, filepath.FromSlash(file))
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		pruneEmptyParents(root, filepath.Dir(name), models)
	}

	return nil
}

// fetchToStage downloads, verifies and extracts one artifact into a fresh
// stage directory, returning its path.
func (i *Installer) fetchToStage(
	ctx context.Context, name, url, sha string, size int64, progress Reporter,
) (string, error) {
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

	if fetchErr := i.fetchArchive(ctx, archive, name, url, sha, size, progress); fetchErr != nil {
		return "", fetchErr
	}

	progress.report(Progress{Phase: PhaseInstall, Item: name})
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

	if err := extractArchive(reader, stage); err != nil {
		removeTreeQuietly(stage)

		return "", fmt.Errorf("extract %s: %w", name, err)
	}

	return stage, nil
}

func (i *Installer) fetchArchive(
	ctx context.Context, archive *os.File, name, url, sha string, size int64, progress Reporter,
) error {
	if err := i.client.Fetch(ctx, name, url, sha, size, archive, progress); err != nil {
		closeQuietly(archive)

		return err
	}
	progress.report(Progress{Phase: PhaseVerify, Item: name})
	if err := archive.Sync(); err != nil {
		closeQuietly(archive)

		return fmt.Errorf("sync download: %w", err)
	}

	return archive.Close()
}

// swapBundle publishes a staged runtime, carrying the previous bundle's model
// directories over so installed packs survive the upgrade. The models are
// re-homed only AFTER the new bundle is published, so the sole copy is never
// moved out of the live tree before its replacement exists: if any step fails
// (notably the Windows retire rename, which a running daemon's open helpers
// block with a sharing violation), the models stay put and recoverBundle
// finishes the interrupted swap on the next operation.
func (i *Installer) swapBundle(stage string) error {
	bundle := filepath.Join(i.root, bundleDirName)
	old := filepath.Join(i.root, bundleOldName)

	if _, err := os.Stat(bundle); err == nil {
		if err := os.Rename(bundle, old); err != nil {
			return fmt.Errorf("retire previous bundle: %w", err)
		}
	}
	if err := os.Rename(stage, bundle); err != nil {
		return fmt.Errorf("publish bundle: %w", err)
	}
	if err := i.carryModels(); err != nil {
		return err
	}
	removeTreeQuietly(old)

	return nil
}

// recoverBundle finishes an interrupted swap. A retired bundle with no
// published successor is restored verbatim; a both-present state is an
// interrupted model carry, re-homed into the live bundle before old is dropped.
func (i *Installer) recoverBundle() {
	bundle := filepath.Join(i.root, bundleDirName)
	old := filepath.Join(i.root, bundleOldName)
	if _, err := os.Stat(old); errors.Is(err, fs.ErrNotExist) {
		return
	}
	if _, err := os.Stat(bundle); errors.Is(err, fs.ErrNotExist) {
		if err := os.Rename(old, bundle); err != nil {
			return
		}

		return
	}
	// Both present: an interrupted carry. Re-home the remaining models, then
	// drop old only once its models are safely in the live bundle.
	if err := i.carryModels(); err != nil {
		return
	}
	removeTreeQuietly(old)
}

// carryModels re-homes every model directory entry of the retired bundle into
// the live, already-published one, keeping the live bundle's own entries when
// both exist. It runs only AFTER the successor is published: an interruption
// at any point leaves the sole copy of each model inside a directory
// recoverBundle can find, never in a stage about to be deleted.
//
// This is the runtime publish's one per-file step — the swap itself is two
// renames of direct children of the engine root — so it carries the same
// containment as publishPackFiles: both bundles are children of the engine
// root, and the whole move rides one *os.Root. The successor bundle is
// attacker-shaped input here, models/ included, and only the kernel can tell a
// directory from a link that resolves like one.
func (i *Installer) carryModels() error {
	root, err := os.OpenRoot(i.root)
	if err != nil {
		return fmt.Errorf("open engine root: %w", err)
	}
	defer closeQuietly(root)

	previous := filepath.Join(bundleOldName, modelsDirName)
	names, err := rootDirNames(root, previous)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read previous models: %w", err)
	}

	live := filepath.Join(bundleDirName, modelsDirName)
	for _, name := range names {
		dest := filepath.Join(live, name)
		if _, err := root.Lstat(dest); err == nil {
			continue
		}
		if err := root.MkdirAll(live, 0o700); err != nil {
			return err
		}
		if err := root.Rename(filepath.Join(previous, name), dest); err != nil {
			return fmt.Errorf("carry %s/%s: %w", modelsDirName, name, err)
		}
	}

	return nil
}

// rootDirNames lists one directory's entry names through a root, so no
// component of the path is followed out of it. The handle is released before
// the names are returned: the caller renames the entries out of this very
// directory, which an open handle blocks on Windows.
func rootDirNames(root *os.Root, name string) ([]string, error) {
	dir, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(dir)

	return dir.Readdirnames(-1)
}

// publishPackFiles moves staged pack files into the live bundle, replacing any
// previous copies. Stage and bundle are both children of the engine root, so
// the whole move rides one *os.Root: every component of both paths is resolved
// with openat-relative syscalls, so a symlink anywhere in either prefix is
// refused by the kernel instead of traversed. That is the containment, and it
// has to live here: the bundle legitimately holds links the runtime shipped,
// and a link the kernel resolves elsewhere is indistinguishable from a
// directory to any check made on the path string.
//
// Rename replaces the destination atomically on all three platforms, so a
// reinstall never leaves the daemon a window in which the model is absent, and
// a model-sized file is never copied.
func (i *Installer) publishPackFiles(stage string, files []string) error {
	stageName, err := filepath.Rel(i.root, stage)
	if err != nil {
		return fmt.Errorf("locate stage: %w", err)
	}
	root, err := os.OpenRoot(i.root)
	if err != nil {
		return fmt.Errorf("open engine root: %w", err)
	}
	defer closeQuietly(root)

	for _, file := range files {
		relative := filepath.FromSlash(file)
		dest := filepath.Join(bundleDirName, relative)
		if err := root.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := root.Rename(filepath.Join(stageName, relative), dest); err != nil {
			return fmt.Errorf("publish %s: %w", file, err)
		}
	}

	return nil
}

// stagedModelFiles lists a staged pack's files, holding every entry to the
// shared pack-file rule (modelFileEntry) at the PRODUCE side of the
// inventory: nothing enters a Files list that the rule would refuse.
func stagedModelFiles(stage string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(stage, func(entry string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		relative, err := filepath.Rel(stage, entry)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(relative)
		if err := modelFileEntry(slashed, d.Type()); err != nil {
			return err
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

// modelFileEntry is the pack-file rule, applied on BOTH sides of the
// inventory: stagedModelFiles holds every file an archive is about to
// publish to it, and readState re-applies it to every file a persisted
// inventory claims, so a state file edited between runs cannot re-admit
// what staging refused. The rule: the entry must be a regular file — a pack
// carries data, so a symlink, device node, socket or fifo has no legitimate
// place in it — named by a clean bundle-relative slash path under models/,
// so a pack can never touch runtime executables. The runtime bundle is the
// opposite case — it does ship symlinks, the versioned dylib sonames — and
// it is published by swapping the whole tree, never through a Files list.
func modelFileEntry(name string, typ fs.FileMode) error {
	if typ != 0 {
		return fmt.Errorf("entry %q is not a regular file: a model pack carries data only", name)
	}
	if !cleanRelativeSlashPath(name) || !strings.HasPrefix(name, modelsDirName+"/") {
		return fmt.Errorf("entry %q is outside %s/", name, modelsDirName)
	}

	return nil
}

// parentRef is the path element that renames where a path points.
const parentRef = ".."

// cleanRelativeSlashPath reports whether name is the canonical slash form
// the installer records: relative, no backslash to smuggle a separator past
// the slash checks, and no empty, "." or parent element to rewrite where
// the name points.
func cleanRelativeSlashPath(name string) bool {
	if name == "" || strings.ContainsRune(name, '\\') {
		return false
	}
	for element := range strings.SplitSeq(name, "/") {
		if element == "" || element == "." || element == parentRef {
			return false
		}
	}

	return true
}

// packFilesPresent reports whether every file of an installed pack is still
// on disk, so a partially deleted pack reinstalls instead of no-oping. It
// probes through the engine root like the publish and the removal, and does
// not follow the final component either: a name that resolves only by leaving
// the root, or that is a link where a published file belongs, is not a file
// this install owns — it counts as absent, and the republish that follows
// fails loudly against the same kernel handle.
func (i *Installer) packFilesPresent(pack *InstalledPack) bool {
	root, err := os.OpenRoot(i.root)
	if err != nil {
		return false
	}
	defer closeQuietly(root)

	for _, file := range pack.Files {
		if _, err := root.Lstat(filepath.Join(bundleDirName, filepath.FromSlash(file))); err != nil {
			return false
		}
	}

	return true
}

// lock serializes engine operations across prukka processes: setup and the
// daemon must never stage over each other. Holding the lock is owning an open
// file handle with the kernel's exclusive lock on it: the kernel releases it
// on unlock and on process death alike, so a live operation can never be
// stolen and a crashed holder is recovered from instantly — no staleness
// clock, and therefore no window in which two processes both reclaim it.
func (i *Installer) lock() (func(), error) {
	if err := os.MkdirAll(i.root, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", i.root, err)
	}
	lock, err := acquireLockFile(filepath.Join(i.root, lockName))
	if err != nil {
		return nil, err
	}
	// Every locked operation self-heals an interrupted swap first, so a crash
	// mid-swap is repaired on the next install/upgrade/pack op — not only when
	// EnsureRuntime happens to run, and never on the lock-free read path.
	i.recoverBundle()

	return func() { closeQuietly(lock) }, nil
}

// acquireLockFile opens the lock file and takes the kernel lock on it,
// reporting ErrBusy against a live holder. The file itself is deliberately
// never unlinked: the lock lives in the handle, not in the name, and a
// remove-and-recreate would let two processes lock two different inodes for
// the same path and both believe they hold the operation lock.
func acquireLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		closeQuietly(f)

		return nil, err
	}

	return f, nil
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

// nativeHelpers are the compiled tools a runtime bundle must contain. The daemon
// self-executes its own engine subcommands, which spawn these helpers from the
// bundle root; the bundle no longer carries an orchestrator binary of its own.
func nativeHelpers() []string {
	suffix := ""
	if runtime.GOOS == hostos.Windows {
		suffix = ".exe"
	}

	helpers := []string{
		enginebundle.WhisperServer + suffix,
		enginebundle.MT + suffix,
		filepath.Join(enginebundle.PiperDir, enginebundle.PiperExe+suffix),
	}
	if runtime.GOOS == hostos.Darwin {
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
		info, err := os.Stat(filepath.Join(dir, helper))
		if err != nil || !hostos.Executable(info) {
			return false
		}
	}

	return true
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

// pruneEmptyParents removes the directories a removal emptied, from dir up to
// (and excluding) stop, through the root that owns them: the names are
// root-relative, so a symlinked component is refused by the kernel rather than
// climbed past. The bound is a path boundary, not a string prefix, and the
// climb is best effort — the first directory that is not empty ends it.
func pruneEmptyParents(root *os.Root, dir, stop string) {
	for strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if err := root.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

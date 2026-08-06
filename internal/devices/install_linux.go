//go:build linux

package devices

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// payloadSrc names the embedded kernel-module source archive.
const payloadSrc = "src"

const modulesLoadConf = "/etc/modules-load.d/prukka.conf"

// secureBootHint is the one next step when the kernel refuses a module.
const secureBootHint = "the kernel refused the module (Secure Boot?) — sign and enroll it via MOK, see docs/DEVICES.md"

// modules maps each kind to its module name and source subdirectory.
var modules = map[Kind]struct{ Name, Dir string }{
	Microphone: {Name: "snd_prukka_mic", Dir: "microphone"},
	Speaker:    {Name: "snd_prukka_speaker", Dir: "audio"},
	Webcam:     {Name: "prukka_webcam", Dir: "webcam"},
}

// install compiles the modules against the running kernel, puts them in the
// extra tree and loads them now and at boot.
func install(ctx context.Context) ([]Result, error) {
	pay, err := payloads()
	if err != nil {
		return nil, err
	}

	data := pay[payloadSrc]

	kernel, kernelErr := kernelRelease(ctx)
	if kernelErr != nil {
		return nil, kernelErr
	}

	// The marker carries the kernel: an upgrade must re-run the build even
	// when the payload is unchanged.
	sum := linuxMarker(data, kernel)
	if current(moduleRoot, kernel, sum) {
		return uniformResults(StateSkipped, ""), nil
	}

	if hint := missingToolchain(kernel); hint != "" {
		return uniformResults(StateManual, hint), nil
	}

	if buildErr := buildModules(ctx, data, kernel); buildErr != nil {
		return nil, buildErr
	}

	return loadModules(ctx, sum)
}

// buildModules extracts the sources, compiles each module and installs the
// .ko files into the kernel's extra tree.
func buildModules(ctx context.Context, data []byte, kernel string) error {
	srcDir := filepath.Join(devicesDir(), "src")
	if err := os.RemoveAll(srcDir); err != nil {
		return fmt.Errorf("clear previous module sources: %w", err)
	}

	if err := extract(data, devicesDir()); err != nil {
		return fmt.Errorf("unpack module sources: %w", err)
	}

	extraDir := moduleTree(kernel) + "/extra"
	if err := os.MkdirAll(extraDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w (%s)", extraDir, err, privilegeHint)
	}

	kdir := moduleTree(kernel) + "/build"

	for _, kind := range kinds() {
		mod := modules[kind]

		dir := filepath.Join(srcDir, mod.Dir)
		if err := runTool(ctx, "make", "-C", dir, "KDIR="+kdir); err != nil {
			return fmt.Errorf("build %s module: %w", kind, err)
		}

		if err := copyFile(filepath.Join(dir, mod.Name+".ko"), moduleFile(moduleRoot, kernel, mod.Name)); err != nil {
			return err
		}
	}

	return runTool(ctx, "depmod", "-a")
}

// loadModules records a module's marker only once it actually loads, and
// registers boot loading only when every module did, so a Secure Boot machine
// stays "missing" instead of failing its load on every boot.
func loadModules(ctx context.Context, sum string) ([]Result, error) {
	results := make([]Result, 0, 3)
	loaded := 0

	for _, kind := range kinds() {
		result := Result{Kind: kind, State: StateInstalled}

		if probeErr := runTool(ctx, "modprobe", modules[kind].Name); probeErr != nil {
			result = Result{Kind: kind, State: StateManual, NextStep: secureBootHint}
		} else {
			loaded++

			if markErr := writeMarker(kind, sum); markErr != nil {
				return nil, markErr
			}
		}

		results = append(results, result)
	}

	if loaded != len(results) {
		return results, nil
	}

	names := make([]string, 0, 3)
	for _, kind := range kinds() {
		names = append(names, modules[kind].Name)
	}

	if err := os.WriteFile(modulesLoadConf, []byte(strings.Join(names, "\n")+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w (%s)", modulesLoadConf, err, privilegeHint)
	}

	return results, nil
}

// remove unloads the modules, deletes them from the extra tree and forgets
// the boot configuration.
func remove(ctx context.Context) ([]Result, error) {
	kernel, kernelErr := kernelRelease(ctx)
	if kernelErr != nil {
		return nil, kernelErr
	}

	loaded, loadErr := loadedModules()
	if loadErr != nil {
		return nil, loadErr
	}

	results, kernels, changed, removeErr := removeModules(ctx, kernel, loaded)
	if removeErr != nil {
		return nil, removeErr
	}

	bootExists, bootErr := bootConfigExists()
	if bootErr != nil {
		return nil, bootErr
	}
	if changed || bootExists {
		if err := forgetBootConfig(); err != nil {
			return nil, err
		}
	}
	if refreshErr := refreshModuleIndexes(ctx, kernels); refreshErr != nil {
		return nil, refreshErr
	}
	if clearErr := clearInstallRecords(); clearErr != nil {
		return nil, clearErr
	}

	return results, nil
}

func removeModules(
	ctx context.Context, kernel string, loaded map[string]bool,
) (results []Result, kernels []string, changed bool, err error) {
	results = make([]Result, 0, 3)
	affectedKernels := make(map[string]struct{})

	for _, kind := range kinds() {
		result, affected, err := removeModule(ctx, kernel, kind, loaded)
		if err != nil {
			return nil, nil, false, err
		}

		changed = changed || result.State == StateRemoved
		for _, version := range affected {
			affectedKernels[version] = struct{}{}
		}
		results = append(results, result)
	}

	kernels = make([]string, 0, len(affectedKernels))
	for affected := range affectedKernels {
		kernels = append(kernels, affected)
	}

	return results, kernels, changed, nil
}

// removeModule unloads one module when live and deletes every version Prukka
// owns, including those under kernels older than the running one.
func removeModule(
	ctx context.Context, kernel string, kind Kind, loaded map[string]bool,
) (Result, []string, error) {
	mod := modules[kind]
	hadMarker := recordedSum(kind) != ""
	wasLoaded := loaded[mod.Name]
	affected := make([]string, 0, 2)

	if wasLoaded {
		if unloadErr := runTool(ctx, "modprobe", "-r", mod.Name); unloadErr != nil {
			return Result{}, nil, unloadErr
		}
		affected = append(affected, kernel)
	}

	paths, kernels, err := deleteInstalledModuleFiles(kind, mod.Name)
	if err != nil {
		return Result{}, nil, err
	}
	affected = append(affected, kernels...)

	if markerErr := dropMarker(kind); markerErr != nil {
		return Result{}, nil, markerErr
	}

	state := StateMissing
	if hadMarker || wasLoaded || len(paths) > 0 {
		state = StateRemoved
	}

	return Result{Kind: kind, State: state}, affected, nil
}

func deleteInstalledModuleFiles(
	kind Kind, name string,
) (paths, kernels []string, err error) {
	paths, err = installedModuleFiles(moduleRoot, name)
	if err != nil {
		return nil, nil, err
	}

	kernels = make([]string, 0, len(paths))
	for _, path := range paths {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("remove %s module %s: %w", kind, path, rmErr)
		}
		if version := moduleKernel(moduleRoot, path); version != "" {
			kernels = append(kernels, version)
		}
	}

	return paths, kernels, nil
}

func bootConfigExists() (bool, error) {
	_, err := os.Stat(modulesLoadConf)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	return false, fmt.Errorf("inspect %s: %w", modulesLoadConf, err)
}

func refreshModuleIndexes(ctx context.Context, kernels []string) error {
	sort.Strings(kernels)
	for _, kernel := range kernels {
		if err := runTool(ctx, "depmod", "-a", kernel); err != nil {
			return err
		}
	}

	return nil
}

func forgetBootConfig() error {
	if err := os.Remove(modulesLoadConf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", modulesLoadConf, err)
	}

	return nil
}

// linuxMarker fingerprints one install as payload digest plus the kernel it
// was built against, so a kernel upgrade orphaning the modules is visible.
func linuxMarker(data []byte, kernel string) string {
	if len(data) == 0 {
		return ""
	}

	return payloadSum(data) + "@" + kernel
}

// status reports each device against the bundled sources and running kernel.
func status(ctx context.Context) ([]Result, error) {
	pay, err := payloads()
	if err != nil && !errors.Is(err, ErrNotBundled) {
		return nil, err
	}

	want, kernel := "", ""

	if data := pay[payloadSrc]; len(data) > 0 {
		release, kernelErr := kernelRelease(ctx)
		if kernelErr != nil {
			return nil, kernelErr
		}

		kernel = release
		want = linuxMarker(data, release)
	}

	results := make([]Result, 0, 3)
	for _, kind := range kinds() {
		results = append(results, moduleStatus(kind, want, moduleRoot, kernel))
	}

	return results, nil
}

// moduleStatus classifies one device from its marker and the extra tree; an
// unbundled build has no kernel to check against, so it stays marker-only.
func moduleStatus(kind Kind, want, root, kernel string) Result {
	state := markerState(kind, want)
	if state == StateInstalled && kernel != "" && !moduleFilePresent(root, kernel, kind) {
		return Result{Kind: kind, State: StateMissing, NextStep: missingFilesNextStep}
	}

	return Result{Kind: kind, State: state}
}

// current reports whether every module carries this digest AND its built
// module is still in the extra tree; a marker that outlived its files would
// otherwise skip the rebuild.
func current(root, kernel, sum string) bool {
	for _, kind := range kinds() {
		if recordedSum(kind) != sum || !moduleFilePresent(root, kernel, kind) {
			return false
		}
	}

	return true
}

// moduleFilePresent reports whether one kind's built module is in this
// kernel's extra tree; a .ko under an older kernel does not count.
func moduleFilePresent(root, kernel string, kind Kind) bool {
	_, err := os.Stat(moduleFile(root, kernel, modules[kind].Name))

	return err == nil
}

func uniformResults(state State, next string) []Result {
	results := make([]Result, 0, 3)
	for _, kind := range kinds() {
		results = append(results, Result{Kind: kind, State: state, NextStep: next})
	}

	return results
}

// kernelRelease names the running kernel, which the modules must match.
func kernelRelease(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return "", fmt.Errorf("detect kernel release: %w", err)
	}

	release := strings.TrimSpace(string(out))
	if !kernelReleasePattern.MatchString(release) {
		return "", fmt.Errorf("unexpected kernel release %q", release)
	}

	return release, nil
}

// kernelReleasePattern bounds uname output before it becomes part of a path.
var kernelReleasePattern = regexp.MustCompile(`^[0-9A-Za-z._+-]+$`)

// missingToolchain names the setup step when the kernel build toolchain is
// absent, or "" when ready to build.
func missingToolchain(kernel string) string {
	hint := "install the kernel build toolchain (e.g. apt install build-essential linux-headers-" + kernel +
		"), then re-run: " + InstallHint()

	if _, err := exec.LookPath("make"); err != nil {
		return hint
	}

	if _, err := os.Stat(moduleTree(kernel) + "/build"); err != nil {
		return hint
	}

	return ""
}

func moduleTree(kernel string) string {
	return filepath.Join(moduleRoot, kernel)
}

const moduleRoot = "/lib/modules"

// moduleFile is the path one built module occupies in a kernel's extra tree.
func moduleFile(root, kernel, name string) string {
	return filepath.Join(root, kernel, "extra", name+".ko")
}

func installedModuleFiles(root, name string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "extra", name+".ko"))
	if err != nil {
		return nil, fmt.Errorf("scan installed %s modules: %w", name, err)
	}

	return paths, nil
}

func moduleKernel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 3 || parts[1] != "extra" {
		return ""
	}

	return parts[0]
}

func loadedModules() (map[string]bool, error) {
	out, err := os.ReadFile("/proc/modules")
	if err != nil {
		return nil, fmt.Errorf("read /proc/modules: %w", err)
	}

	loaded := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if name, _, found := strings.Cut(line, " "); found {
			loaded[name] = true
		}
	}

	return loaded, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}

	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("install %s: %w", dst, err)
	}

	return nil
}

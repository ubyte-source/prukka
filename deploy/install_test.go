//go:build !windows

package deploy_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakePrukka = `#!/bin/sh
# %s
[ -z "${PRUKKA_FAKE_COMMAND_LOG:-}" ] || printf '%%s|%%s\n' "$0" "$*" >> "$PRUKKA_FAKE_COMMAND_LOG"
if [ "${1:-}" = setup ]; then exit "${PRUKKA_FAKE_SETUP_STATUS:-0}"; fi
if [ "${1:-}" = stats ]; then exit 1; fi
exit 0
`

type fallbackFixture struct {
	root        string
	binDir      string
	tools       string
	uninstaller string
	state       string
	config      string
	unit        string
	legacyLink  string
	modulesConf string
	deviceState string
	commandLog  string
	env         []string
}

func TestInstallRollbackAndPurgeLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatalf("create tools: %v", err)
	}
	writeExecutable(t, filepath.Join(tools, "id"), "#!/bin/sh\necho 501\n")
	writeExecutable(t, filepath.Join(tools, "sudo"), "#!/bin/sh\nexec \"$@\"\n")
	writeExecutable(t, filepath.Join(tools, "uname"), "#!/bin/sh\n[ \"${1:-}\" = -m ] && echo x86_64 || echo linux\n")
	prepareToolbox(t, tools)

	uninstaller, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	first := fmt.Appendf(nil, fakePrukka, "first")
	archive := writeArchive(t, first, uninstaller)
	env := lifecycleEnv(root, binDir, tools, archive, "0")
	installAndAssert(t, env, binDir, first, uninstaller)

	second := fmt.Appendf(nil, fakePrukka, "second")
	archive = writeArchive(t, second, uninstaller)
	env = lifecycleEnv(root, binDir, tools, archive, "19")
	rollbackAndAssert(t, env, binDir, first, uninstaller)
	state, config := uninstallAndKeepData(t, env, root, binDir)
	env = lifecycleEnv(root, binDir, tools, archive, "0")
	installAndAssert(t, env, binDir, second, uninstaller)
	purgeAndAssert(t, env, state, config, binDir)
}

func TestPrivilegedStageRejectsSourceTOCTOU(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatalf("create tools: %v", err)
	}
	writeExecutable(t, filepath.Join(tools, "id"), "#!/bin/sh\necho 501\n")
	writeExecutable(t, filepath.Join(tools, "sudo"), "#!/bin/sh\nexec \"$@\"\n")
	writeExecutable(t, filepath.Join(tools, "uname"),
		"#!/bin/sh\n[ \"${1:-}\" = -m ] && echo x86_64 || echo linux\n")
	prepareToolbox(t, tools)

	hostInstall, err := exec.LookPath("install")
	if err != nil {
		t.Fatalf("find host install: %v", err)
	}
	if removeErr := os.Remove(filepath.Join(tools, "install")); removeErr != nil {
		t.Fatalf("replace install tool: %v", removeErr)
	}
	writeExecutable(t, filepath.Join(tools, "install"), fmt.Sprintf(`#!/bin/sh
previous=
last=
for arg do previous=$last; last=$arg; done
%q "$@"
case "$last" in
  */privileged/prukka-privileged.*/.release.new)
    printf '\nsource-raced\n' >> "$previous"
    : > "$PRUKKA_TAMPER_MARKER"
    ;;
esac
`, hostInstall))

	uninstaller, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	binary := fmt.Appendf(nil, fakePrukka, "toctou")
	archive := writeArchive(t, binary, uninstaller)
	commandLog := filepath.Join(root, "prukka.log")
	tamperMarker := filepath.Join(root, "tampered")
	env := append(lifecycleEnv(root, binDir, tools, archive, "0"),
		"PRUKKA_FAKE_COMMAND_LOG="+commandLog,
		"PRUKKA_TAMPER_MARKER="+tamperMarker)
	runScript(t, env, "install.sh")

	if _, statErr := os.Stat(tamperMarker); statErr != nil {
		t.Fatalf("TOCTOU hook did not run: %v", statErr)
	}
	commands, err := os.ReadFile(filepath.Clean(commandLog))
	if err != nil {
		t.Fatalf("read fake command log: %v", err)
	}
	if strings.Contains(string(commands), "devices install") {
		t.Fatalf("raced privileged image executed:\n%s", commands)
	}
	entries, err := os.ReadDir(filepath.Join(root, "privileged"))
	if err != nil {
		t.Fatalf("read privileged fixture root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("privileged stage was not cleaned: %v", entries)
	}
}

func TestDeployScriptsContainNoElevatedUserImage(t *testing.T) {
	t.Parallel()
	assertNoElevatedUserImages(t)
	assertWindowsPrivilegeBoundary(t)
	assertUnixPrivilegeBoundary(t)
}

func assertNoElevatedUserImages(t *testing.T) {
	t.Helper()

	checks := map[string][]string{
		"install.sh":   {`privileged "$BIN_DIR/prukka"`, `sudo "$BIN_DIR/prukka"`},
		"uninstall.sh": {`privileged "$prukka"`, `sudo "$prukka"`},
		"uninstall.ps1": {
			`Invoke-Native $exe @("devices"`, `Invoke-Native $exe @("service"`, `& $exe stats`,
		},
	}
	for name, forbidden := range checks {
		body := readDeployScript(t, name)
		for _, fragment := range forbidden {
			if strings.Contains(string(body), fragment) {
				t.Errorf("%s contains elevated user-image pattern %q", name, fragment)
			}
		}
	}
}

func assertWindowsPrivilegeBoundary(t *testing.T) {
	t.Helper()

	installer := readDeployScript(t, "install.ps1")
	for _, required := range []string{
		`[Environment]::GetEnvironmentVariables().Keys`,
		`-like "PRUKKA_*"`,
		`[Environment]::SetEnvironmentVariable([string] $name, $null, "Process")`,
		`$env:ProgramData = $programData`,
		`if ($customArchive) {`,
		`custom archives are never executed elevated`,
		`PrukkaDevicesRemoved`,
	} {
		if !strings.Contains(string(installer), required) {
			t.Errorf("install.ps1 does not sanitize elevated environment: missing %q", required)
		}
	}

	uninstaller := readDeployScript(t, "uninstall.ps1")
	for _, required := range []string{
		`SELECT HardwareID FROM Win32_PnPEntity`,
		`Test-TrustedDeviceCleanupTombstone`,
		`Move-Item -LiteralPath $trusted -Destination $tombstone`,
	} {
		if !strings.Contains(string(uninstaller), required) {
			t.Errorf("uninstall.ps1 lacks retry-safe verified cleanup: missing %q", required)
		}
	}
}

func assertUnixPrivilegeBoundary(t *testing.T) {
	t.Helper()

	unixInstaller := readDeployScript(t, "install.sh")
	for _, required := range []string{
		`[ -n "${PRUKKA_INSTALL_URL:-}" ] && [ "$test_mode" -ne 1 ]`,
		`custom archives are never executed with root privileges`,
		`/usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin`,
		`-C "$root_stage/extract" prukka`,
	} {
		if !strings.Contains(string(unixInstaller), required) {
			t.Errorf("install.sh does not confine custom archives: missing %q", required)
		}
	}
	if strings.Contains(string(unixInstaller), `-xOzf "$root_stage/release.tar.gz" prukka |`) {
		t.Error("install.sh trusts the final status of a privileged extraction pipeline")
	}
	unixUninstaller := readDeployScript(t, "uninstall.sh")
	if !strings.Contains(string(unixUninstaller),
		`/usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin`) {
		t.Error("uninstall.sh does not use a fixed privileged PATH")
	}
}

func readDeployScript(t *testing.T, name string) []byte {
	t.Helper()

	var body []byte
	var err error
	switch name {
	case "install.sh":
		body, err = os.ReadFile("install.sh")
	case "uninstall.sh":
		body, err = os.ReadFile("uninstall.sh")
	case "install.ps1":
		body, err = os.ReadFile("install.ps1")
	case "uninstall.ps1":
		body, err = os.ReadFile("uninstall.ps1")
	default:
		t.Fatalf("unsupported deploy script %q", name)
	}
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return body
}

func TestCustomArchiveRequiresExplicitDigest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatalf("create tools: %v", err)
	}
	writeExecutable(t, filepath.Join(tools, "id"), "#!/bin/sh\necho 501\n")
	writeExecutable(t, filepath.Join(tools, "uname"),
		"#!/bin/sh\n[ \"${1:-}\" = -m ] && echo x86_64 || echo linux\n")
	prepareToolbox(t, tools)
	uninstaller, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	archive := writeArchive(t, fmt.Appendf(nil, fakePrukka, "unpinned"), uninstaller)
	env := lifecycleEnv(root, filepath.Join(root, "bin"), tools, archive, "0")
	filtered := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, "PRUKKA_INSTALL_SHA256=") {
			filtered = append(filtered, item)
		}
	}
	cmd := scriptCommand(filtered, "install.sh")
	if output, err := cmd.CombinedOutput(); err == nil ||
		!strings.Contains(string(output), "PRUKKA_INSTALL_SHA256 is required") {
		t.Fatalf("unpinned custom archive was not rejected: %v\n%s", err, output)
	}
}

func TestUninstallWithoutExecutable(t *testing.T) {
	t.Parallel()

	fixture := newFallbackFixture(t)
	assertIsolatedPath(t, fixture.env, fixture.tools)
	runScript(t, fixture.env, fixture.uninstaller, "--purge")
	fixture.assertRemoved(t)

	commands, readErr := os.ReadFile(filepath.Clean(fixture.commandLog))
	if readErr != nil {
		t.Fatalf("read command log: %v", readErr)
	}
	commandText := string(commands)
	if strings.Contains(commandText, "sudo ") {
		t.Fatalf("fixture test mode crossed the privilege boundary:\n%s", commandText)
	}
	for _, want := range []string{
		"systemctl --user disable --now prukka.service",
		"rm -f " + fixture.modulesConf,
		"rm -rf " + fixture.deviceState,
		"secret-tool clear service prukka username openrouter",
		"secret-tool clear service prukka username cartesia",
	} {
		if !strings.Contains(commandText, want) {
			t.Errorf("command log missing %q:\n%s", want, commandText)
		}
	}
	assertCommandsConfined(t, commandText, fixture.root)
}

func TestFixtureOverridesRequireExplicitTestMode(t *testing.T) {
	t.Parallel()

	fixture := newFallbackFixture(t)
	env := make([]string, 0, len(fixture.env))
	for _, item := range fixture.env {
		if !strings.HasPrefix(item, "PRUKKA_DEPLOY_TEST_MODE=") &&
			!strings.HasPrefix(item, "PRUKKA_TEST_ROOT=") {
			env = append(env, item)
		}
	}
	cmd := scriptCommand(env, fixture.uninstaller, "--purge")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("fixture overrides worked without explicit test mode:\n%s", output)
	}
	for _, path := range []string{fixture.state, fixture.config, fixture.modulesConf, fixture.deviceState} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("rejected override changed %s: %v", path, err)
		}
	}
}

func newFallbackFixture(t *testing.T) fallbackFixture {
	t.Helper()

	root := t.TempDir()
	fixture := fallbackFixture{
		root:        root,
		binDir:      filepath.Join(root, "bin"),
		tools:       filepath.Join(root, "tools"),
		state:       filepath.Join(root, "prukka"),
		config:      filepath.Join(root, ".config", "prukka", "config.yaml"),
		unit:        filepath.Join(root, ".config", "systemd", "user", "prukka.service"),
		legacyLink:  filepath.Join(root, "system", "usr", "local", "bin", "prukka"),
		modulesConf: filepath.Join(root, "system", "etc", "modules-load.d", "prukka.conf"),
		deviceState: filepath.Join(root, "system", "var", "lib", "prukka", "devices"),
		commandLog:  filepath.Join(root, "commands.log"),
	}
	fixture.uninstaller = filepath.Join(fixture.binDir, "prukka-uninstall")
	procModules := filepath.Join(root, "system", "proc", "modules")

	prepareFallbackDirectories(t, &fixture, procModules)
	prepareFallbackToolbox(t, fixture.tools)
	prepareFallbackFiles(t, &fixture, procModules)

	missingPrukka := filepath.Join(fixture.binDir, "missing-prukka")
	if linkErr := os.Symlink(missingPrukka, fixture.legacyLink); linkErr != nil {
		t.Fatalf("create legacy link: %v", linkErr)
	}
	fixture.env = lifecycleEnv(root, fixture.binDir, fixture.tools, "", "0")
	fixture.env = append(fixture.env,
		"PRUKKA_STATE="+fixture.state,
		"PRUKKA_CONFIG="+fixture.config,
		"PRUKKA_BIN="+missingPrukka,
		"PRUKKA_TEST_COMMAND_LOG="+fixture.commandLog,
		"PRUKKA_TEST_ROOT="+root,
	)

	return fixture
}

func prepareFallbackDirectories(t *testing.T, fixture *fallbackFixture, procModules string) {
	t.Helper()

	directories := []string{
		fixture.binDir,
		fixture.tools,
		fixture.state,
		filepath.Dir(fixture.config),
		filepath.Dir(fixture.unit),
		filepath.Dir(fixture.legacyLink),
		filepath.Dir(fixture.modulesConf),
		fixture.deviceState,
		filepath.Dir(procModules),
	}
	for _, dir := range directories {
		if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
			t.Fatalf("create isolated directory %s: %v", dir, mkdirErr)
		}
	}
}

func prepareFallbackFiles(t *testing.T, fixture *fallbackFixture, procModules string) {
	t.Helper()

	uninstaller, readErr := os.ReadFile("uninstall.sh")
	if readErr != nil {
		t.Fatalf("read uninstaller: %v", readErr)
	}
	writeExecutable(t, fixture.uninstaller, string(uninstaller))
	writeFixtureFile(t, filepath.Join(fixture.state, "sentinel"), []byte("state"), 0o600)
	writeFixtureFile(t, fixture.config, []byte("config"), 0o600)
	writeFixtureFile(t, fixture.unit, []byte("unit"), 0o600)
	writeFixtureFile(t, fixture.modulesConf, []byte("modules"), 0o600)
	writeFixtureFile(t, filepath.Join(fixture.deviceState, "marker"), []byte("marker"), 0o600)
	writeFixtureFile(t, procModules, nil, 0o600)
}

func prepareFallbackToolbox(t *testing.T, tools string) {
	t.Helper()

	writeExecutable(t, filepath.Join(tools, "id"), `#!/bin/sh
printf 'id %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
printf '501\n'
`)
	writeExecutable(t, filepath.Join(tools, "uname"), `#!/bin/sh
printf 'uname %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
printf 'linux\n'
`)
	writeExecutable(t, filepath.Join(tools, "tr"), `#!/bin/sh
printf 'tr %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
while IFS= read -r line; do printf '%s\n' "$line"; done
`)
	writeExecutable(t, filepath.Join(tools, "sudo"), `#!/bin/sh
printf 'sudo %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
case "${1:-}" in
  rm | rmdir) exec "$@" ;;
  *) printf 'UNSAFE sudo %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"; exit 97 ;;
esac
`)
	writeExecutable(t, filepath.Join(tools, "pgrep"), `#!/bin/sh
printf 'pgrep %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
exit 1
`)
	writeExecutable(t, filepath.Join(tools, "systemctl"), `#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
[ "${2:-}" = is-active ] && exit 1
exit 0
`)
	writeExecutable(t, filepath.Join(tools, "secret-tool"), `#!/bin/sh
printf 'secret-tool %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
[ "${1:-}" = lookup ] && exit 1
exit 0
`)
	writeExecutable(t, filepath.Join(tools, "prukka"), `#!/bin/sh
printf 'decoy-prukka %s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
exit 91
`)

	for _, name := range []string{"grep", "mv", "readlink", "rm", "rmdir"} {
		writeGuardedTool(t, tools, name)
	}
}

func writeGuardedTool(t *testing.T, tools, name string) {
	t.Helper()

	hostTool, lookupErr := exec.LookPath(name)
	if lookupErr != nil {
		t.Fatalf("find host %s for guarded fixture: %v", name, lookupErr)
	}
	body := fmt.Sprintf(`#!/bin/sh
printf '%s %%s\n' "$*" >> "$PRUKKA_TEST_COMMAND_LOG"
for arg do
  case "$arg" in
    -*) ;;
    /*)
      case "$arg" in
        "$PRUKKA_TEST_ROOT" | "$PRUKKA_TEST_ROOT"/*) ;;
        *)
          printf 'UNSAFE %s %%s\n' "$arg" >> "$PRUKKA_TEST_COMMAND_LOG"
          exit 97
          ;;
      esac
      ;;
  esac
done
exec %q "$@"
`, name, name, hostTool)
	writeExecutable(t, filepath.Join(tools, name), body)
}

func (fixture *fallbackFixture) assertRemoved(t *testing.T) {
	t.Helper()

	paths := []string{
		fixture.state,
		fixture.config,
		fixture.unit,
		fixture.uninstaller,
		fixture.legacyLink,
		fixture.modulesConf,
		fixture.deviceState,
	}
	for _, path := range paths {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Errorf("fallback uninstall residue %s: %v", path, statErr)
		}
	}
}

func assertIsolatedPath(t *testing.T, env []string, tools string) {
	t.Helper()

	want := "PATH=" + tools
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			if item != want {
				t.Fatalf("fixture inherited a host PATH: got %q, want %q", item, want)
			}
			return
		}
	}
	t.Fatal("fixture environment has no PATH")
}

func assertCommandsConfined(t *testing.T, commandText, root string) {
	t.Helper()

	if strings.Contains(commandText, "decoy-prukka") || strings.Contains(commandText, "UNSAFE ") {
		t.Fatalf("fallback invoked a forbidden command:\n%s", commandText)
	}
	for field := range strings.FieldsSeq(commandText) {
		if !filepath.IsAbs(field) {
			continue
		}
		relative, relErr := filepath.Rel(root, field)
		outside := relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator))
		if relErr != nil || outside {
			t.Fatalf("fallback invoked an external path %q:\n%s", field, commandText)
		}
	}
}

func TestPurgeRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	t.Run("external config", testPurgeRejectsExternalConfig)
	t.Run("symlink state", testPurgeRejectsSymlinkState)
	t.Run("traversal state", testPurgeRejectsTraversalState)
	t.Run("symlink ancestor", testPurgeRejectsSymlinkAncestor)
}

func testPurgeRejectsExternalConfig(t *testing.T) {
	t.Parallel()

	root, binDir, env := uninstallFixture(t)
	state := filepath.Join(root, "prukka")
	external := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}
	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+external)
	assertPurgeRefused(t, env, filepath.Join(binDir, "prukka-uninstall"),
		[]string{state, external, filepath.Join(binDir, "prukka")})
}

func testPurgeRejectsSymlinkState(t *testing.T) {
	t.Parallel()

	root, binDir, env := uninstallFixture(t)
	outside := filepath.Join(t.TempDir(), "Prukka")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("create outside state: %v", err)
	}
	state := filepath.Join(root, "Prukka")
	if err := os.Symlink(outside, state); err != nil {
		t.Fatalf("link state: %v", err)
	}
	config := filepath.Join(root, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(config, []byte("config"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+config)
	assertPurgeRefused(t, env, filepath.Join(binDir, "prukka-uninstall"),
		[]string{state, outside, config, filepath.Join(binDir, "prukka")})
}

func testPurgeRejectsTraversalState(t *testing.T) {
	t.Parallel()

	root, binDir, env := uninstallFixture(t)
	actualState := filepath.Join(root, "Prukka")
	if err := os.MkdirAll(actualState, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	state := strings.Join([]string{root, "nested", "..", "Prukka"}, string(os.PathSeparator))
	config := filepath.Join(root, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(config, []byte("config"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+config)
	assertPurgeRefused(t, env, filepath.Join(binDir, "prukka-uninstall"),
		[]string{actualState, config, filepath.Join(binDir, "prukka")})
}

func testPurgeRejectsSymlinkAncestor(t *testing.T) {
	t.Parallel()

	root, binDir, env := uninstallFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	stateOutside := filepath.Join(outside, "Prukka")
	if err := os.MkdirAll(stateOutside, 0o700); err != nil {
		t.Fatalf("create outside state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateOutside, "sentinel"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	ancestor := filepath.Join(root, "linked")
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Fatalf("create ancestor symlink: %v", err)
	}
	state := filepath.Join(ancestor, "Prukka")
	config := filepath.Join(root, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(config, []byte("config"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+config)
	assertPurgeRefused(t, env, filepath.Join(binDir, "prukka-uninstall"),
		[]string{ancestor, stateOutside, filepath.Join(stateOutside, "sentinel"), config,
			filepath.Join(binDir, "prukka")})
}

func uninstallFixture(t *testing.T) (root, binDir string, env []string) {
	t.Helper()

	root = t.TempDir()
	binDir = filepath.Join(root, "bin")
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatalf("create tools: %v", err)
	}
	writeExecutable(t, filepath.Join(tools, "id"), "#!/bin/sh\necho 501\n")
	prepareToolbox(t, tools)
	writeExecutable(t, filepath.Join(binDir, "prukka"), fmt.Sprintf(fakePrukka, "unsafe"))
	uninstaller, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "prukka-uninstall"), string(uninstaller))

	return root, binDir, lifecycleEnv(root, binDir, tools, "", "0")
}

func assertPurgeRefused(t *testing.T, env []string, uninstaller string, preserved []string) {
	t.Helper()

	cmd := scriptCommand(env, uninstaller, "--purge")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unsafe purge succeeded:\n%s", output)
	}
	for _, path := range preserved {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("unsafe purge changed %s: %v", path, err)
		}
	}
}

func installAndAssert(t *testing.T, env []string, binDir string, binary, uninstaller []byte) {
	t.Helper()

	runScript(t, env, "install.sh")
	assertFile(t, filepath.Join(binDir, "prukka"), binary)
	assertFile(t, filepath.Join(binDir, "prukka-uninstall"), uninstaller)
}

func rollbackAndAssert(t *testing.T, env []string, binDir string, binary, uninstaller []byte) {
	t.Helper()

	cmd := scriptCommand(env, "install.sh")
	if output, runErr := cmd.CombinedOutput(); runErr == nil {
		t.Fatalf("failing setup succeeded:\n%s", output)
	}
	assertFile(t, filepath.Join(binDir, "prukka"), binary)
	assertFile(t, filepath.Join(binDir, "prukka-uninstall"), uninstaller)
	assertNoInstallerScratch(t, binDir)
}

func uninstallAndKeepData(t *testing.T, env []string, root, binDir string) (state, config string) {
	t.Helper()

	state = filepath.Join(root, "prukka")
	config = filepath.Join(root, "config", "config.yaml")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "sentinel"), []byte("state"), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := os.WriteFile(config, []byte("config"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+config, "PRUKKA_FAKE_SETUP_STATUS=0")
	runScript(t, env, filepath.Join(binDir, "prukka-uninstall"))
	for _, path := range []string{state, config} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("default uninstall removed retained data %s: %v", path, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(binDir, "prukka")); !os.IsNotExist(statErr) {
		t.Errorf("default uninstall left executable: %v", statErr)
	}

	return state, config
}

func purgeAndAssert(t *testing.T, env []string, state, config, binDir string) {
	t.Helper()

	env = append(env, "PRUKKA_STATE="+state, "PRUKKA_CONFIG="+config, "PRUKKA_FAKE_SETUP_STATUS=0")
	runScript(t, env, filepath.Join(binDir, "prukka-uninstall"), "--purge")
	for _, path := range []string{filepath.Join(binDir, "prukka"), state, config} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("purge residue %s: %v", path, statErr)
		}
	}
}

func writeArchive(t *testing.T, binary, uninstaller []byte) string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), fmt.Sprintf("release-%d-*.tar.gz", len(binary)))
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	path := f.Name()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, data := range map[string][]byte{"prukka": binary, "deploy/uninstall.sh": uninstaller} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatalf("write %s header: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	return path
}

func lifecycleEnv(root, binDir, tools, archive, setupStatus string) []string {
	env := make([]string, 0, len(os.Environ())+15)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "HOME=") && !strings.HasPrefix(item, "PATH=") &&
			!strings.HasPrefix(item, "PRUKKA_") && !strings.HasPrefix(item, "XDG_CONFIG_HOME=") &&
			!strings.HasPrefix(item, "XDG_STATE_HOME=") && !strings.HasPrefix(item, "XDG_RUNTIME_DIR=") {
			env = append(env, item)
		}
	}

	env = append(env,
		"HOME="+root,
		"PATH="+tools,
		"PRUKKA_DEPLOY_TEST_MODE=prukka-deploy-fixtures-v1",
		"PRUKKA_TEST_ROOT="+root,
		"PRUKKA_BIN_DIR="+binDir,
		"PRUKKA_LEGACY_LINK="+filepath.Join(root, "system", "usr", "local", "bin", "prukka"),
		"PRUKKA_INSTALL_URL=file://"+archive,
		"PRUKKA_FAKE_SETUP_STATUS="+setupStatus,
		"PRUKKA_LINUX_MODULE_ROOT="+filepath.Join(root, "system", "lib", "modules"),
		"PRUKKA_LINUX_PROC_MODULES="+filepath.Join(root, "system", "proc", "modules"),
		"PRUKKA_LINUX_MODULES_LOAD_CONF="+filepath.Join(root, "system", "etc", "modules-load.d", "prukka.conf"),
		"PRUKKA_DEVICE_STATE_DIR="+filepath.Join(root, "system", "var", "lib", "prukka", "devices"),
		"XDG_CONFIG_HOME="+filepath.Join(root, ".config"),
		"XDG_STATE_HOME="+filepath.Join(root, ".local", "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"),
	)
	if archive != "" {
		contents, err := os.ReadFile(filepath.Clean(archive))
		if err != nil {
			panic(fmt.Sprintf("read lifecycle archive: %v", err))
		}
		digest := sha256.Sum256(contents)
		env = append(env, fmt.Sprintf("PRUKKA_INSTALL_SHA256=%x", digest))
	}

	return env
}

func prepareToolbox(t *testing.T, dir string) {
	t.Helper()

	writeExecutable(t, filepath.Join(dir, "security"), `#!/bin/sh
[ "${1:-}" = find-generic-password ] && exit 1
exit 0
`)
	writeExecutable(t, filepath.Join(dir, "secret-tool"), `#!/bin/sh
[ "${1:-}" = lookup ] && exit 1
exit 0
`)

	for _, name := range []string{
		"awk", "cat", "chmod", "cp", "curl", "grep", "gzip", "install", "ln", "mkdir", "mktemp",
		"mv", "readlink", "rm", "rmdir", "sysctl", "tar", "tee", "tr", "uname",
	} {
		dest := filepath.Join(dir, name)
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect tool %s: %v", dest, err)
		}

		source, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("required host tool %s: %v", name, err)
		}
		if err := os.Symlink(source, dest); err != nil {
			t.Fatalf("link tool %s: %v", name, err)
		}
	}

	hashTool := "sha256sum"
	if _, err := exec.LookPath(hashTool); err != nil {
		hashTool = "shasum"
	}
	source, err := exec.LookPath(hashTool)
	if err != nil {
		t.Fatalf("required SHA-256 tool: %v", err)
	}
	writeExecutable(t, filepath.Join(dir, hashTool), fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", source))
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	writeFixtureFile(t, path, []byte(body), 0o700)
}

func writeFixtureFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()

	dir, openErr := os.OpenRoot(filepath.Dir(path))
	if openErr != nil {
		t.Fatalf("open parent of %s: %v", path, openErr)
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			t.Errorf("close parent of %s: %v", path, closeErr)
		}
	}()

	name := filepath.Base(path)
	if err := dir.WriteFile(name, body, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := dir.Chmod(name, mode); err != nil {
		t.Fatalf("set mode on %s: %v", path, err)
	}
}

func runScript(t *testing.T, env []string, script string, args ...string) {
	t.Helper()

	cmd := scriptCommand(env, script, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", script, args, err, output)
	}
}

func scriptCommand(env []string, script string, args ...string) *exec.Cmd {
	argv := append([]string{script}, args...)
	cmd := exec.CommandContext(context.Background(), "sh", argv...)
	cmd.Env = env

	return cmd
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()

	got, err := os.ReadFile(filepath.Clean(path))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s = %q (%v), want %q", path, got, err, want)
	}
}

func assertNoInstallerScratch(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read install dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".prukka") {
			t.Errorf("installer scratch survived: %s", entry.Name())
		}
	}
}

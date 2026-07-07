//go:build windows

package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPowerShellLifecycleScriptsParse(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"install.ps1", "uninstall.ps1"} {
		path, err := filepath.Abs(name)
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		path = strings.ReplaceAll(path, "'", "''")
		program := `$tokens=$null; $errors=$null; ` +
			`[void][System.Management.Automation.Language.Parser]::ParseFile('` + path +
			`',[ref]$tokens,[ref]$errors); if($errors.Count){$errors | Out-String | Write-Error; exit 1}`
		cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Errorf("%s does not parse: %v\n%s", name, runErr, output)
		}
	}
}

func TestPowerShellUninstallWithoutExecutable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "fallback.log")
	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}

	quote := func(value string) string {
		return strings.ReplaceAll(value, "'", "''")
	}
	program := strings.Join([]string{
		`$env:USERPROFILE='` + quote(root) + `'`,
		`$env:LOCALAPPDATA='` + quote(filepath.Join(root, "local")) + `'`,
		`$env:APPDATA='` + quote(filepath.Join(root, "roaming")) + `'`,
		`$env:PRUKKA_BIN_DIR='` + quote(binDir) + `'`,
		`. '` + quote(script) + `'`,
		`function Assert-Elevated {}`,
		`function Remove-ServiceFallback { [IO.File]::AppendAllText('` +
			quote(logPath) + `', "service" + [Environment]::NewLine) }`,
		`function Remove-DevicesFallback { [IO.File]::AppendAllText('` +
			quote(logPath) + `', "devices" + [Environment]::NewLine) }`,
		`function Remove-UserPathEntry([string] $BinDir) { [IO.File]::AppendAllText('` +
			quote(logPath) + `', "path" + [Environment]::NewLine) }`,
		`$Purge=$false`,
		`Invoke-PrukkaUninstall`,
	}, "; ")
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("missing-executable fallback failed: %v\n%s", runErr, output)
	}

	log, err := os.ReadFile(filepath.Clean(logPath))
	if err != nil {
		t.Fatalf("read fallback log: %v", err)
	}
	if got, want := strings.ReplaceAll(string(log), "\r\n", "\n"), "service\ndevices\npath\n"; got != want {
		t.Fatalf("fallback calls = %q, want %q", got, want)
	}
}

func TestPowerShellPurgeRejectsJunctionAncestor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "Prukka"), 0o700); err != nil {
		t.Fatalf("create outside state: %v", err)
	}
	junction := filepath.Join(root, "linked")
	cmd := exec.CommandContext(t.Context(), "cmd.exe", "/d", "/c", "mklink", "/J", junction, outside)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("junction creation unavailable: %v\n%s", err, output)
	}

	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}
	quote := func(value string) string { return strings.ReplaceAll(value, "'", "''") }
	program := strings.Join([]string{
		`$env:USERPROFILE='` + quote(root) + `'`,
		`. '` + quote(script) + `'`,
		`try { Assert-OwnedDirectory '` + quote(filepath.Join(junction, "Prukka")) + `'; exit 9 } catch { exit 0 }`,
	}, "; ")
	cmd = exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction ancestor was accepted: %v\n%s", err, output)
	}
}

func TestPowerShellUninstallContainsNoUserImageExecution(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("uninstall.ps1")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	for _, forbidden := range []string{
		`Invoke-Native $exe @("service"`, `Invoke-Native $exe @("devices"`, `& $exe stats`,
	} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("uninstaller contains elevated user-image pattern %q", forbidden)
		}
	}
}

func TestPowerShellAudioFallbackUsesExactIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logPath := filepath.Join(root, "audio.log")
	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}
	quote := func(value string) string {
		return strings.ReplaceAll(value, "'", "''")
	}
	appendCall := `[IO.File]::AppendAllText('` + quote(logPath) + `', ($Arguments -join ' ') + [Environment]::NewLine)`
	program := strings.Join([]string{
		`. '` + quote(script) + `'`,
		`function Invoke-NativeBestEffort([string] $File, [string[]] $Arguments) { ` + appendCall + ` }`,
		`function Invoke-Native([string] $File, [string[]] $Arguments) { ` + appendCall + ` }`,
		`$script:audioInventoryCalls=0`,
		`function Test-AudioDevicePresent([string] $HardwareId) { ` +
			`$script:audioInventoryCalls++; ` +
			`return (($script:audioInventoryCalls % 2) -eq 1) }`,
		`function Get-SystemWindowsDrivers { @(` +
			`[pscustomobject]@{ProviderName='Prukka';OriginalFileName='C:\store\prukka_mic.inf';Driver='oem3.inf'},` +
			`[pscustomobject]@{ProviderName='Other';OriginalFileName='C:\store\prukka_speaker.inf';Driver='oem4.inf'},` +
			`[pscustomobject]@{ProviderName='Prukka';OriginalFileName='C:\store\other.inf';Driver='oem5.inf'}) }`,
		`Remove-AudioFallback`,
	}, "; ")
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("audio fallback failed: %v\n%s", runErr, output)
	}

	log, err := os.ReadFile(filepath.Clean(logPath))
	if err != nil {
		t.Fatalf("read audio log: %v", err)
	}
	got := strings.ReplaceAll(string(log), "\r\n", "\n")
	want := "/remove-device /deviceid Root\\PrukkaMic\n" +
		"/remove-device /deviceid Root\\PrukkaSpeaker\n" +
		"/delete-driver oem3.inf /uninstall\n"
	if got != want {
		t.Fatalf("audio fallback calls = %q, want %q", got, want)
	}
}

func TestPowerShellAudioFallbackReportsRemoveFailure(t *testing.T) {
	t.Parallel()

	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}
	quote := func(value string) string {
		return strings.ReplaceAll(value, "'", "''")
	}
	program := strings.Join([]string{
		`. '` + quote(script) + `'`,
		`function Invoke-Native([string] $File, [string[]] $Arguments) { throw 'forced pnputil failure' }`,
		`function Test-AudioDevicePresent([string] $HardwareId) { return $true }`,
		`function Get-SystemWindowsDrivers { @() }`,
		`try { Remove-AudioFallback; exit 9 } catch { ` +
			`if ($_.Exception.Message -notlike 'residue:*Root\PrukkaMic*') { ` +
			`Write-Error $_; exit 8 }; exit 0 }`,
	}, "; ")
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("audio removal failure was not reported as residue: %v\n%s", runErr, output)
	}
}

func TestPowerShellAudioFallbackIsIdempotentWhenDevicesAreAbsent(t *testing.T) {
	t.Parallel()

	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}
	quote := func(value string) string {
		return strings.ReplaceAll(value, "'", "''")
	}
	program := strings.Join([]string{
		`. '` + quote(script) + `'`,
		`function Invoke-Native([string] $File, [string[]] $Arguments) { throw 'pnputil must not run for an absent device' }`,
		`function Test-AudioDevicePresent([string] $HardwareId) { return $false }`,
		`function Get-SystemWindowsDrivers { @() }`,
		`Remove-AudioFallback`,
	}, "; ")
	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("absent audio devices were not idempotent: %v\n%s", runErr, output)
	}
}

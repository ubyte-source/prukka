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
		if output, runErr := runPowerShell(t, program); runErr != nil {
			t.Errorf("%s does not parse: %v\n%s", name, runErr, output)
		}
	}
}

func TestPowerShellUninstallWithoutExecutable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "fallback.log")
	script := uninstallScript(t)

	program := strings.Join([]string{
		`$env:USERPROFILE='` + psQuote(root) + `'`,
		`$env:LOCALAPPDATA='` + psQuote(filepath.Join(root, "local")) + `'`,
		`$env:APPDATA='` + psQuote(filepath.Join(root, "roaming")) + `'`,
		`$env:PRUKKA_BIN_DIR='` + psQuote(binDir) + `'`,
		`. '` + psQuote(script) + `'`,
		`function Assert-Elevated {}`,
		`function Remove-ServiceFallback { [IO.File]::AppendAllText('` +
			psQuote(logPath) + `', "service" + [Environment]::NewLine) }`,
		`function Remove-DevicesFallback { [IO.File]::AppendAllText('` +
			psQuote(logPath) + `', "devices" + [Environment]::NewLine) }`,
		`function Remove-UserPathEntry([string] $BinDir) { [IO.File]::AppendAllText('` +
			psQuote(logPath) + `', "path" + [Environment]::NewLine) }`,
		`$Purge=$false`,
		`Invoke-PrukkaUninstall`,
	}, "; ")
	output, runErr := runPowerShell(t, program)
	if runErr != nil {
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

	script := uninstallScript(t)
	program := strings.Join([]string{
		`$env:USERPROFILE='` + psQuote(root) + `'`,
		`. '` + psQuote(script) + `'`,
		`try { Assert-OwnedDirectory '` + psQuote(filepath.Join(junction, "Prukka")) + `'; exit 9 } catch { exit 0 }`,
	}, "; ")
	if output, err := runPowerShell(t, program); err != nil {
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
	script := uninstallScript(t)
	appendCall := `[IO.File]::AppendAllText('` + psQuote(logPath) + `', ($Arguments -join ' ') + [Environment]::NewLine)`
	program := strings.Join([]string{
		`. '` + psQuote(script) + `'`,
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
	output, runErr := runPowerShell(t, program)
	if runErr != nil {
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

	script := uninstallScript(t)
	program := strings.Join([]string{
		`. '` + psQuote(script) + `'`,
		`function Invoke-Native([string] $File, [string[]] $Arguments) { throw 'forced pnputil failure' }`,
		`function Test-AudioDevicePresent([string] $HardwareId) { return $true }`,
		`function Get-SystemWindowsDrivers { @() }`,
		`try { Remove-AudioFallback; exit 9 } catch { ` +
			`if ($_.Exception.Message -notlike 'residue:*Root\PrukkaMic*') { ` +
			`Write-Error $_; exit 8 }; exit 0 }`,
	}, "; ")
	output, runErr := runPowerShell(t, program)
	if runErr != nil {
		t.Fatalf("audio removal failure was not reported as residue: %v\n%s", runErr, output)
	}
}

func TestPowerShellAudioFallbackIsIdempotentWhenDevicesAreAbsent(t *testing.T) {
	t.Parallel()

	script := uninstallScript(t)
	program := strings.Join([]string{
		`. '` + psQuote(script) + `'`,
		`function Invoke-Native([string] $File, [string[]] $Arguments) { throw 'pnputil must not run for an absent device' }`,
		`function Test-AudioDevicePresent([string] $HardwareId) { return $false }`,
		`function Get-SystemWindowsDrivers { @() }`,
		`Remove-AudioFallback`,
	}, "; ")
	output, runErr := runPowerShell(t, program)
	if runErr != nil {
		t.Fatalf("absent audio devices were not idempotent: %v\n%s", runErr, output)
	}
}

// psQuote escapes a value for a single-quoted PowerShell string.
func psQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// uninstallScript resolves the committed uninstall.ps1 the tests dot-source.
func uninstallScript(t *testing.T) string {
	t.Helper()

	script, err := filepath.Abs("uninstall.ps1")
	if err != nil {
		t.Fatalf("resolve uninstaller: %v", err)
	}

	return script
}

// runPowerShell runs one non-interactive PowerShell program and returns its
// combined output.
func runPowerShell(t *testing.T, program string) ([]byte, error) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", program)

	return cmd.CombinedOutput()
}

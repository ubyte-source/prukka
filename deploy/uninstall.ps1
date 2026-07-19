param(
    [switch] $Purge,
    [switch] $DevicesOnly
)

# Remove Prukka on Windows. By default user configuration, state, logs and
# legacy provider credentials are retained; -Purge removes those too. Launch
# the LocalAppData copy without elevation; it delegates to the verified,
# Administrator-owned ProgramData copy installed with the release.
$ErrorActionPreference = "Stop"
$selfPath = $PSCommandPath

function Invoke-Native([string] $File, [string[]] $Arguments) {
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$File $($Arguments -join ' ') exited with status $LASTEXITCODE"
    }
}

function Invoke-NativeBestEffort([string] $File, [string[]] $Arguments) {
    try {
        & $File @Arguments *> $null
    } catch {
        return
    }
}

function Assert-NoReparseComponents([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "refusing an empty path"
    }
    if ($Path.Split(@('\', '/')) -contains '..') {
        throw "refusing a path with traversal: $Path"
    }

    $full = [System.IO.Path]::GetFullPath($Path)
    $root = [System.IO.Path]::GetPathRoot($full)
    $current = $root
    foreach ($component in $full.Substring($root.Length).Split(@('\', '/'),
            [StringSplitOptions]::RemoveEmptyEntries)) {
        $current = Join-Path $current $component
        if (-not (Test-Path -LiteralPath $current)) { break }
        if ((Get-Item -LiteralPath $current -Force).Attributes -band
            [System.IO.FileAttributes]::ReparsePoint) {
            throw "refusing path through reparse point: $current"
        }
    }
}

function Assert-NoReparseTree([string] $Path) {
    Assert-NoReparseComponents $Path
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }

    $pending = [System.Collections.Generic.Stack[string]]::new()
    $pending.Push([System.IO.Path]::GetFullPath($Path))
    while ($pending.Count -ne 0) {
        $directory = $pending.Pop()
        foreach ($item in Get-ChildItem -LiteralPath $directory -Force) {
            if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
                throw "refusing recursive removal containing reparse point: $($item.FullName)"
            }
            if ($item.PSIsContainer) { $pending.Push($item.FullName) }
        }
    }
}

function Remove-CheckedTree([string] $Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Assert-NoReparseTree $Path
    $parent = Split-Path -Parent $Path
    Assert-NoReparseComponents $parent
    $quarantine = Join-Path $parent (".prukka-remove-" + [Guid]::NewGuid().ToString("N"))
    Move-Item -LiteralPath $Path -Destination $quarantine
    Assert-NoReparseComponents $parent
    Assert-NoReparseTree $quarantine
    Remove-Item -LiteralPath $quarantine -Recurse -Force
}

function Test-DeployFixtureMode {
    if ([string]::IsNullOrEmpty($env:PRUKKA_DEPLOY_TEST_MODE)) {
        if ($env:PRUKKA_TEST_ROOT) { throw "PRUKKA_TEST_ROOT requires explicit deploy test mode" }
        return $false
    }
    if ($env:PRUKKA_DEPLOY_TEST_MODE -ne "prukka-deploy-fixtures-v1" -or
        [string]::IsNullOrWhiteSpace($env:PRUKKA_TEST_ROOT)) {
        throw "invalid deploy test mode"
    }

    $actual = [System.IO.Path]::GetFullPath([Environment]::GetFolderPath(
        [Environment+SpecialFolder]::UserProfile)).TrimEnd('\', '/')
    $testRoot = [System.IO.Path]::GetFullPath($env:PRUKKA_TEST_ROOT).TrimEnd('\', '/')
    if (-not $testRoot.StartsWith($actual + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "deploy test root must remain below the OS user profile"
    }
    Assert-NoReparseComponents $testRoot
    return $true
}

function Get-ProfileRoot {
    if (Test-DeployFixtureMode) {
        return ([System.IO.Path]::GetFullPath($env:PRUKKA_TEST_ROOT).TrimEnd('\', '/'))
    }
    $profile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    Assert-NoReparseComponents $profile
    return ([System.IO.Path]::GetFullPath($profile).TrimEnd('\', '/'))
}

function Assert-ProfilePath([string] $Path, [string] $Purpose) {
    Assert-NoReparseComponents $Path
    $full = [System.IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    $profile = Get-ProfileRoot
    if (-not $full.StartsWith($profile + '\', [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing $Purpose outside the user profile: $Path"
    }
}

function Assert-OwnedDirectory([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "refusing to purge an empty path"
    }

    $trimmed = $Path.TrimEnd('\', '/')
    $leaf = [System.IO.Path]::GetFileName($trimmed)
    if ($leaf -ne "Prukka") {
        throw "refusing to purge unsafe directory: $Path"
    }

    Assert-ProfilePath $trimmed "purge directory"
    if ((Test-Path -LiteralPath $trimmed) -and
        -not (Test-Path -LiteralPath $trimmed -PathType Container)) {
        throw "refusing to purge a non-directory: $Path"
    }
}

function Assert-OwnedConfig([string] $Path) {
    if ([System.IO.Path]::GetFileName($Path) -ne "config.yaml") {
        throw "refusing to purge unsafe config path: $Path"
    }
    Assert-ProfilePath $Path "config"
    if ((Test-Path -LiteralPath $Path) -and
        -not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "refusing to purge a non-file config path: $Path"
    }
}

function Remove-ServiceFallback {
    $scheduler = Join-Path ([Environment]::SystemDirectory) "schtasks.exe"
    Assert-NoReparseComponents $scheduler
    try {
        & $scheduler /End /TN Prukka *> $null
        & $scheduler /Delete /TN Prukka /F *> $null
        & $scheduler /Query /TN Prukka *> $null
        if ($LASTEXITCODE -eq 0) {
            throw "scheduled task Prukka is still registered"
        }
    } catch {
        throw "residue: scheduled task Prukka could not be removed: $($_.Exception.Message)"
    }
}

function Get-SystemWindowsDrivers {
    $windows = [System.IO.Directory]::GetParent([Environment]::SystemDirectory).FullName
    $dismModule = Join-Path $windows "System32\WindowsPowerShell\v1.0\Modules\Dism\Dism.psd1"
    Assert-NoReparseComponents $dismModule
    Import-Module -Name $dismModule -Force -ErrorAction Stop
    return @(Dism\Get-WindowsDriver -Online -All)
}

function Test-AudioDevicePresent([string] $HardwareId) {
    Add-Type -AssemblyName System.Management
    $searcher = [System.Management.ManagementObjectSearcher]::new(
        "SELECT HardwareID FROM Win32_PnPEntity")
    $devices = $null
    try {
        $devices = $searcher.Get()
        foreach ($device in $devices) {
            foreach ($candidate in @($device["HardwareID"])) {
                if ([string]::Equals([string] $candidate, $HardwareId,
                        [StringComparison]::OrdinalIgnoreCase)) {
                    return $true
                }
            }
        }
        return $false
    }
    finally {
        if ($null -ne $devices) { $devices.Dispose() }
        $searcher.Dispose()
    }
}

function Remove-AudioFallback {
    $pnputil = Join-Path ([Environment]::SystemDirectory) "pnputil.exe"
    Assert-NoReparseComponents $pnputil
    foreach ($hardwareId in @("Root\PrukkaMic", "Root\PrukkaSpeaker")) {
        try { $present = Test-AudioDevicePresent $hardwareId }
        catch {
            throw "residue: virtual audio device $hardwareId could not be inventoried: $($_.Exception.Message)"
        }
        if (-not $present) { continue }
        try {
            Invoke-Native $pnputil @("/remove-device", "/deviceid", $hardwareId)
        } catch {
            throw "residue: virtual audio device $hardwareId could not be removed: $($_.Exception.Message)"
        }
        try { $present = Test-AudioDevicePresent $hardwareId }
        catch {
            throw "residue: virtual audio device $hardwareId could not be verified after removal: $($_.Exception.Message)"
        }
        if ($present) {
            throw "residue: virtual audio device $hardwareId is still present after pnputil removal"
        }
    }

    try {
        $packages = @(Get-SystemWindowsDrivers | Where-Object {
            $_.ProviderName -eq "Prukka" -and
            [System.IO.Path]::GetFileName($_.OriginalFileName) -in @("prukka_mic.inf", "prukka_speaker.inf")
        })
    } catch {
        throw "residue: Windows Driver Store packages for Root\PrukkaMic and Root\PrukkaSpeaker could not be inventoried: $($_.Exception.Message)"
    }

    foreach ($publishedName in @($packages | Select-Object -ExpandProperty Driver -Unique)) {
        if ($publishedName -notmatch '^oem[0-9]+\.inf$') {
            throw "residue: Prukka audio package has unsafe Published Name: $publishedName"
        }
        Invoke-Native $pnputil @("/delete-driver", $publishedName, "/uninstall")
    }
}

function Remove-WebcamFallback {
    $programData = [Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)
    $devices = Join-Path $programData "Prukka\devices"
    Assert-NoReparseComponents $programData
    Assert-NoReparseComponents $devices

    # Never execute the staged controller while elevated: older installations
    # may have left it writable by the installing user. Fixed system tools and
    # exact registry/file identities are sufficient for the fallback.
    $taskkill = Join-Path ([Environment]::SystemDirectory) "taskkill.exe"
    Assert-NoReparseComponents $taskkill
    Invoke-NativeBestEffort $taskkill @("/F", "/T", "/IM", "PrukkaWebcamCtl.exe")

    $classKey = "HKLM:\Software\Classes\CLSID\{81530786-7639-4DEF-BB04-85C9482CD274}"
    if (Test-Path -LiteralPath $classKey) {
        Remove-Item -LiteralPath $classKey -Recurse -Force
    }
    $dll = Join-Path $programData "Prukka\PrukkaWebcam.dll"
    Assert-NoReparseComponents $dll
    if (Test-Path -LiteralPath $dll) {
        Remove-Item -LiteralPath $dll -Force
    }
    if (Test-Path -LiteralPath $devices) {
        Remove-CheckedTree $devices
    }

    $parent = Join-Path $programData "Prukka"
    Assert-NoReparseComponents $parent
    if ((Test-Path -LiteralPath $parent -PathType Container) -and
        @(Get-ChildItem -LiteralPath $parent -Force).Count -eq 0) {
        Remove-Item -LiteralPath $parent -Force
    }
}

function Remove-DevicesFallback {
    $errors = [System.Collections.Generic.List[string]]::new()
    try { Remove-AudioFallback } catch { [void] $errors.Add($_.Exception.Message) }
    try { Remove-WebcamFallback } catch { [void] $errors.Add("residue: Prukka Webcam registration or files: $($_.Exception.Message)") }
    if ($errors.Count -ne 0) {
        throw ($errors -join [Environment]::NewLine)
    }
}

function Remove-LegacyCredentials {
    if (-not ("Prukka.NativeCredential" -as [type])) {
        Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
namespace Prukka {
    public static class NativeCredential {
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern bool CredDelete(string target, int type, int flags);
    }
}
'@
    }

    foreach ($account in @("openrouter", "cartesia")) {
        if (-not [Prukka.NativeCredential]::CredDelete("prukka:$account", 1, 0)) {
            $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
            if ($code -ne 1168) {
                throw "residue: legacy Credential Manager target prukka:$account (Win32 error $code)"
            }
        }
    }
}

function Assert-Elevated {
    if (-not (Test-IsElevated)) {
        throw "privileged cleanup must run from the trusted ProgramData uninstaller"
    }
}

function Test-IsElevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal] $identity).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-TrustedUninstallerPath {
    $programData = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::CommonApplicationData)
    return (Join-Path $programData "PrukkaUninstall.ps1")
}

function Get-DeviceCleanupTombstonePath {
    $programData = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::CommonApplicationData)
    return (Join-Path $programData "PrukkaDevicesRemoved")
}

function Assert-TrustedUninstaller([string] $Path) {
    Assert-NoReparseComponents $Path
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "trusted ProgramData uninstaller is unavailable"
    }

    $acl = Get-Acl -LiteralPath $Path
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    $trustedWriters = @("S-1-5-18", "S-1-5-32-544")
    if ($owner -notin $trustedWriters -or -not $acl.AreAccessRulesProtected) {
        throw "trusted ProgramData uninstaller has an unsafe owner or inherited ACL"
    }
    $dangerous = [Security.AccessControl.FileSystemRights]::WriteData -bor
        [Security.AccessControl.FileSystemRights]::AppendData -bor
        [Security.AccessControl.FileSystemRights]::WriteExtendedAttributes -bor
        [Security.AccessControl.FileSystemRights]::WriteAttributes -bor
        [Security.AccessControl.FileSystemRights]::Delete -bor
        [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership
    $rules = $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])
    foreach ($rule in $rules) {
        if ($rule.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            $rule.IdentityReference.Value -notin $trustedWriters -and
            ($rule.FileSystemRights -band $dangerous)) {
            throw "trusted ProgramData uninstaller is writable by $($rule.IdentityReference.Value)"
        }
    }
}

function Test-TrustedDeviceCleanupTombstone {
    $tombstone = Get-DeviceCleanupTombstonePath
    Assert-NoReparseComponents $tombstone
    if (-not (Test-Path -LiteralPath $tombstone)) { return $false }
    Assert-TrustedUninstaller $tombstone
    return $true
}

function Invoke-TrustedDeviceCleanup {
    $trusted = Get-TrustedUninstallerPath
    try { Assert-TrustedUninstaller $trusted }
    catch {
        Write-Warning "trusted privileged uninstaller unavailable: $($_.Exception.Message)"
        return -1
    }

    $pathToken = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($trusted))
    $command = @"
`$ErrorActionPreference = "Stop"
`$path = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("$pathToken"))
try { & `$path -DevicesOnly } catch { Write-Error `$_; exit 1 }
"@
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
    $hostExe = Join-Path ([Environment]::SystemDirectory) "WindowsPowerShell\v1.0\powershell.exe"
    Assert-NoReparseComponents $hostExe
    try {
        $process = Start-Process -FilePath $hostExe -Verb RunAs -Wait -PassThru -ArgumentList @(
            "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
            "-EncodedCommand", $encoded)
    }
    catch {
        Write-Warning "privileged uninstall was cancelled or failed: $($_.Exception.Message)"
        return -1
    }
    return $process.ExitCode
}

function Remove-UserPathEntry([string] $BinDir) {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $keptPath = @(($userPath -split ';') | Where-Object {
        -not [string]::IsNullOrWhiteSpace($_) -and $_.TrimEnd('\') -ne $BinDir.TrimEnd('\')
    }) -join ';'
    if ($keptPath -ne $userPath) {
        [Environment]::SetEnvironmentVariable("Path", $keptPath, "User")
    }
}

function Invoke-PrukkaUninstall(
    [switch] $SkipPrivilegedDevices,
    [switch] $DevicesAlreadyRemoved
) {
    if ($SkipPrivilegedDevices -and $DevicesAlreadyRemoved) {
        throw "invalid device-cleanup state"
    }
    if (-not $SkipPrivilegedDevices -and -not $DevicesAlreadyRemoved) { Assert-Elevated }

    $fixtureMode = Test-DeployFixtureMode
    $localAppData = if ($fixtureMode -and $env:LOCALAPPDATA) {
        $env:LOCALAPPDATA
    } else {
        [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    }
    $appData = if ($fixtureMode -and $env:APPDATA) {
        $env:APPDATA
    } else {
        [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
    }
    Assert-ProfilePath $localAppData "LocalAppData"
    Assert-ProfilePath $appData "AppData"

    $binDir = if ($env:PRUKKA_BIN_DIR) {
        $env:PRUKKA_BIN_DIR
    } else {
        Join-Path $localAppData "Prukka\bin"
    }
    Assert-ProfilePath $binDir "binary directory"
    if ($env:PRUKKA_BIN) {
        throw "PRUKKA_BIN is disabled: the elevated uninstaller removes only the expected installed image"
    }
    if (-not $fixtureMode -and ($env:PRUKKA_STATE -or $env:PRUKKA_CONFIG)) {
        throw "state/config path overrides require PRUKKA_DEPLOY_TEST_MODE=prukka-deploy-fixtures-v1"
    }
    $exe = Join-Path $binDir "prukka.exe"

    $state = if ($env:PRUKKA_STATE) { $env:PRUKKA_STATE } else { Join-Path $localAppData "Prukka" }
    $config = if ($env:PRUKKA_CONFIG) {
        $env:PRUKKA_CONFIG
    } else {
        Join-Path $appData "Prukka\config.yaml"
    }

    if ($Purge) {
        Assert-OwnedDirectory $state
        Assert-OwnedConfig $config
    }

    $cleanupErrors = [System.Collections.Generic.List[string]]::new()

    Write-Host "==> removing the per-user service"
    try { Remove-ServiceFallback } catch { [void] $cleanupErrors.Add($_.Exception.Message) }

    $foreground = @(Get-Process -Name prukka -ErrorAction SilentlyContinue |
        Select-Object -ExpandProperty Id)
    if ($foreground.Count -ne 0) {
        [void] $cleanupErrors.Add("residue: foreground Prukka process(es): $($foreground -join ', ')")
    }

    Write-Host "==> removing virtual devices"
    if ($DevicesAlreadyRemoved) {
        Write-Host "    trusted device cleanup completed"
    } elseif ($SkipPrivilegedDevices) {
        [void] $cleanupErrors.Add(
            "residue: virtual devices require the trusted ProgramData uninstaller; rerun the verified installer")
    } else {
        try { Remove-DevicesFallback } catch { [void] $cleanupErrors.Add($_.Exception.Message) }
    }

    if ($Purge) {
        Write-Host "==> removing legacy provider credentials"
        try { Remove-LegacyCredentials } catch { [void] $cleanupErrors.Add($_.Exception.Message) }
    }


    Remove-UserPathEntry $binDir

    Assert-ProfilePath $binDir "binary directory"
    Assert-NoReparseComponents $exe
    if (Test-Path -LiteralPath $exe) {
        Remove-Item -LiteralPath $exe -Force -ErrorAction Stop
    }
    foreach ($name in @("prukka.exe.old", "prukka.exe.new")) {
        $candidate = Join-Path $binDir $name
        Assert-NoReparseComponents $candidate
        Remove-Item -LiteralPath $candidate -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $binDir -PathType Container) {
        Get-ChildItem -LiteralPath $binDir -Force -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -like "prukka.exe.install-old-*" } |
            ForEach-Object {
                Assert-NoReparseComponents $_.FullName
                Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue
            }
    }

    if ($Purge) {
        Write-Host "==> purging configuration, state and logs"
        Assert-OwnedDirectory $state
        Assert-OwnedConfig $config
        if (Test-Path -LiteralPath $config) {
            Remove-Item -LiteralPath $config -Force
        }
        if (Test-Path -LiteralPath $state) {
            Remove-CheckedTree $state
        }

        $configDir = Split-Path -Parent $config
        Assert-ProfilePath $configDir "config directory"
        if ((Test-Path -LiteralPath $configDir -PathType Container) -and
            @(Get-ChildItem -LiteralPath $configDir -Force).Count -eq 0) {
            Remove-Item -LiteralPath $configDir -Force
        }
    }

    $installedUninstaller = Join-Path $binDir "prukka-uninstall.ps1"
    if (Test-Path -LiteralPath $binDir -PathType Container) {
        Get-ChildItem -LiteralPath $binDir -Force -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -like "prukka-uninstall.ps1.install-old-*" } |
            ForEach-Object {
                Assert-NoReparseComponents $_.FullName
                Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue
            }
    }
    if ($cleanupErrors.Count -ne 0) {
        throw ("Prukka user files were removed, but system cleanup left these residues:`n" +
            ($cleanupErrors -join "`n"))
    }

    if ($selfPath -eq $installedUninstaller -and (Test-Path -LiteralPath $selfPath)) {
        Remove-Item -LiteralPath $selfPath -Force
    }
    if ((Test-Path -LiteralPath $binDir -PathType Container) -and
        @(Get-ChildItem -LiteralPath $binDir -Force).Count -eq 0) {
        Remove-Item -LiteralPath $binDir -Force
    }

    Write-Host "Prukka uninstalled."
    if (-not $Purge) {
        Write-Host "Configuration, state, logs and legacy provider credentials were retained; use -Purge to remove them."
    }

}

function Invoke-TrustedDevicesOnly {
    if ($Purge) { throw "-Purge cannot be combined with the internal -DevicesOnly mode" }
    Assert-Elevated
    $trusted = Get-TrustedUninstallerPath
    Assert-TrustedUninstaller $trusted
    if ([System.IO.Path]::GetFullPath($selfPath) -ne [System.IO.Path]::GetFullPath($trusted)) {
        throw "refusing elevated device cleanup from a user-writable uninstaller"
    }

    Remove-DevicesFallback
    $tombstone = Get-DeviceCleanupTombstonePath
    Assert-NoReparseComponents $tombstone
    if (Test-Path -LiteralPath $tombstone) {
        Assert-TrustedUninstaller $tombstone
        Remove-Item -LiteralPath $tombstone -Force
    }
    Move-Item -LiteralPath $trusted -Destination $tombstone
    Assert-TrustedUninstaller $tombstone
    Write-Host "Prukka virtual devices removed."
}

if ($MyInvocation.InvocationName -ne '.') {
    if ($DevicesOnly) {
        if (-not (Test-IsElevated)) {
            throw "-DevicesOnly is reserved for the trusted elevated helper"
        }
        Invoke-TrustedDevicesOnly
    } elseif (Test-IsElevated) {
        throw "refusing elevated execution from a user-writable uninstaller; run it without elevation"
    } else {
        $tombstoneStatus = 0
        try { $devicesAlreadyRemoved = Test-TrustedDeviceCleanupTombstone }
        catch {
            $devicesAlreadyRemoved = $false
            $tombstoneStatus = -1
            Write-Warning "unsafe device-cleanup completion record: $($_.Exception.Message)"
        }
        if ($devicesAlreadyRemoved) {
            Invoke-PrukkaUninstall -DevicesAlreadyRemoved
        } elseif ($tombstoneStatus -ne 0) {
            Invoke-PrukkaUninstall -SkipPrivilegedDevices
        } else {
            $status = Invoke-TrustedDeviceCleanup
            if ($status -eq 0) {
                Invoke-PrukkaUninstall -DevicesAlreadyRemoved
            } else {
                Write-Warning "continuing with unprivileged cleanup; virtual devices will be reported as residue"
                Invoke-PrukkaUninstall -SkipPrivilegedDevices
            }
        }
    }
}

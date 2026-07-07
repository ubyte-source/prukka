# Prukka one-command installer for Windows (PowerShell):
#
#   irm https://prukka.ubyte.it/install.ps1 | iex
#
# Downloads the release binary, installs ffmpeg automatically (prukka
# setup), registers the per-user service and uses a verified, ACL-protected UAC
# stage for virtual devices. Run this script from a non-elevated shell.
$ErrorActionPreference = "Stop"

function Remove-OldImage([string] $Path) {
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        if (-not (Test-Path $Path)) { return }
        try {
            Remove-Item $Path -Force -ErrorAction Stop
            return
        }
        catch {
            Start-Sleep -Milliseconds 100
        }
    }

    Write-Warning "old executable remains at $Path; no process should still be using it"
}

function Invoke-Native([string] $File, [string[]] $Arguments) {
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$File $($Arguments -join ' ') exited with status $LASTEXITCODE"
    }
}

function Test-IsElevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal] $identity).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-NoReparseComponents([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { throw "empty path" }
    if ($Path.Split(@('\', '/')) -contains '..') {
        throw "refusing path with traversal: $Path"
    }
    $full = [IO.Path]::GetFullPath($Path)
    $root = [IO.Path]::GetPathRoot($full)
    $current = $root
    foreach ($component in $full.Substring($root.Length).Split(@('\', '/'),
            [StringSplitOptions]::RemoveEmptyEntries)) {
        $current = Join-Path $current $component
        if (-not (Test-Path -LiteralPath $current)) { break }
        if ((Get-Item -LiteralPath $current -Force).Attributes -band
            [IO.FileAttributes]::ReparsePoint) {
            throw "refusing path through reparse point $current"
        }
    }
}

function Assert-NoReparseTree([string] $Path) {
    Assert-NoReparseComponents $Path
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $pending = [System.Collections.Generic.Stack[string]]::new()
    $pending.Push([IO.Path]::GetFullPath($Path))
    while ($pending.Count -ne 0) {
        $directory = $pending.Pop()
        foreach ($item in Get-ChildItem -LiteralPath $directory -Force) {
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "refusing recursive removal containing reparse point $($item.FullName)"
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
    $actual = [IO.Path]::GetFullPath([Environment]::GetFolderPath(
        [Environment+SpecialFolder]::UserProfile)).TrimEnd('\', '/')
    $testRoot = [IO.Path]::GetFullPath($env:PRUKKA_TEST_ROOT).TrimEnd('\', '/')
    if (-not $testRoot.StartsWith($actual + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "deploy test root must remain below the OS user profile"
    }
    Assert-NoReparseComponents $testRoot
    return $true
}

function Get-ProfileRoot {
    if (Test-DeployFixtureMode) {
        return ([IO.Path]::GetFullPath($env:PRUKKA_TEST_ROOT).TrimEnd('\', '/'))
    }
    $profile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    Assert-NoReparseComponents $profile
    return ([IO.Path]::GetFullPath($profile).TrimEnd('\', '/'))
}

function Assert-UserProfilePath([string] $Path, [string] $Purpose) {
    Assert-NoReparseComponents $Path
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    $profile = Get-ProfileRoot
    if (-not $full.StartsWith($profile + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing $Purpose path outside the user profile: $Path"
    }
}

function Get-FileSha256([string] $Path) {
    $stream = [IO.FileStream]::new($Path, [IO.FileMode]::Open,
        [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
    }
    finally {
        $sha.Dispose()
        $stream.Dispose()
    }
}

function Save-Download([string] $Uri, [string] $Path) {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $client = [Net.WebClient]::new()
    try { $client.DownloadFile($Uri, $Path) }
    finally { $client.Dispose() }
}

# The elevated process receives only a pinned archive identity. It copies that
# archive into an Administrator/SYSTEM-only ProgramData directory, verifies it
# again, extracts one exact entry, atomically activates it, and only then runs
# `devices install`.
function Invoke-PrivilegedDeviceInstall([string] $ArchivePath, [string] $ExpectedHash) {
    $archiveToken = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(
        [System.IO.Path]::GetFullPath($ArchivePath)))
    $command = @'
$ErrorActionPreference = "Stop"
$source = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("__ARCHIVE__"))
$expected = "__HASH__"

function Assert-NoReparseComponent([string] $Path) {
    $full = [IO.Path]::GetFullPath($Path)
    $root = [IO.Path]::GetPathRoot($full)
    $current = $root
    foreach ($component in $full.Substring($root.Length).Split(@('\', '/'),
            [StringSplitOptions]::RemoveEmptyEntries)) {
        $current = Join-Path $current $component
        if (-not (Test-Path -LiteralPath $current)) { break }
        if ((Get-Item -LiteralPath $current -Force).Attributes -band
            [IO.FileAttributes]::ReparsePoint) {
            throw "refusing privileged staging through reparse point $current"
        }
    }
}

function Get-Sha256([string] $Path) {
    $stream = [IO.FileStream]::new($Path, [IO.FileMode]::Open,
        [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
    }
    finally {
        $sha.Dispose()
        $stream.Dispose()
    }
}

function New-AdminDirectory([string] $Path) {
    $admins = [Security.Principal.SecurityIdentifier]::new("S-1-5-32-544")
    $system = [Security.Principal.SecurityIdentifier]::new("S-1-5-18")
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($admins)
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
        [Security.AccessControl.InheritanceFlags]::ObjectInherit
    foreach ($sid in @($admins, $system)) {
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $sid, [Security.AccessControl.FileSystemRights]::FullControl, $inherit,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow)
        [void] $acl.AddAccessRule($rule)
    }
    $directory = [IO.DirectoryInfo]::new($Path)
    $directory.Create($acl)
}

function Set-AdminFileAcl([string] $Path, [bool] $AllowUsersRead = $false) {
    $admins = [Security.Principal.SecurityIdentifier]::new("S-1-5-32-544")
    $system = [Security.Principal.SecurityIdentifier]::new("S-1-5-18")
    $acl = [Security.AccessControl.FileSecurity]::new()
    $acl.SetOwner($admins)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @($admins, $system)) {
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $sid, [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow)
        [void] $acl.AddAccessRule($rule)
    }
    if ($AllowUsersRead) {
        $users = [Security.Principal.SecurityIdentifier]::new("S-1-5-32-545")
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $users, [Security.AccessControl.FileSystemRights]::ReadAndExecute,
            [Security.AccessControl.AccessControlType]::Allow)
        [void] $acl.AddAccessRule($rule)
    }
    [IO.File]::SetAccessControl($Path, $acl)
}

function Assert-AdminOwnedFile([string] $Path) {
    Assert-NoReparseComponent $Path
    $acl = [IO.File]::GetAccessControl($Path)
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    $trusted = @("S-1-5-18", "S-1-5-32-544")
    if ($owner -notin $trusted -or -not $acl.AreAccessRulesProtected) {
        throw "privileged completion record has unsafe ownership or ACL inheritance"
    }
    $dangerous = [Security.AccessControl.FileSystemRights]::WriteData -bor
        [Security.AccessControl.FileSystemRights]::AppendData -bor
        [Security.AccessControl.FileSystemRights]::Delete -bor
        [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership
    foreach ($rule in $acl.GetAccessRules($true, $true,
            [Security.Principal.SecurityIdentifier])) {
        if ($rule.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            $rule.IdentityReference.Value -notin $trusted -and
            ($rule.FileSystemRights -band $dangerous)) {
            throw "privileged completion record is writable by $($rule.IdentityReference.Value)"
        }
    }
}

$programData = [Environment]::GetFolderPath(
    [Environment+SpecialFolder]::CommonApplicationData)
Assert-NoReparseComponent $programData
$stage = Join-Path $programData ("PrukkaPrivileged-" + [Guid]::NewGuid().ToString("N"))
New-AdminDirectory $stage
Assert-NoReparseComponent $stage

try {
    $pendingArchive = Join-Path $stage ".release.new"
    $trustedArchive = Join-Path $stage "release.zip"
    [IO.File]::Copy($source, $pendingArchive, $false)
    if ((Get-Sha256 $pendingArchive) -ne $expected -or
        (Get-Sha256 $source) -ne $expected) {
        throw "archive identity changed during privileged copy"
    }
    Set-AdminFileAcl $pendingArchive
    Move-Item -LiteralPath $pendingArchive -Destination $trustedArchive
    if ((Get-Sha256 $trustedArchive) -ne $expected) {
        throw "archive identity changed after privileged activation"
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($trustedArchive)
    try {
        $entries = @($zip.Entries | Where-Object {
            $_.FullName -eq "prukka.exe" -and $_.Name -eq "prukka.exe"
        })
        if ($entries.Count -ne 1 -or $entries[0].Length -le 0 -or
            $entries[0].Length -gt 200MB) {
            throw "archive must contain exactly one bounded top-level prukka.exe"
        }
        $uninstallerEntries = @($zip.Entries | Where-Object {
            $_.FullName -eq "deploy/uninstall.ps1" -and $_.Name -eq "uninstall.ps1"
        })
        if ($uninstallerEntries.Count -ne 1 -or $uninstallerEntries[0].Length -le 0 -or
            $uninstallerEntries[0].Length -gt 1MB) {
            throw "archive must contain exactly one bounded deploy/uninstall.ps1"
        }
        $pendingExe = Join-Path $stage ".prukka.new"
        $stream = $entries[0].Open()
        $output = [IO.FileStream]::new($pendingExe, [IO.FileMode]::CreateNew,
            [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $stream.CopyTo($output) }
        finally { $output.Dispose(); $stream.Dispose() }

        $pendingUninstaller = Join-Path $stage ".uninstall.new"
        $stream = $uninstallerEntries[0].Open()
        $output = [IO.FileStream]::new($pendingUninstaller, [IO.FileMode]::CreateNew,
            [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $stream.CopyTo($output) }
        finally { $output.Dispose(); $stream.Dispose() }
    }
    finally { $zip.Dispose() }

    $exeDigest = Get-Sha256 $pendingExe
    Set-AdminFileAcl $pendingExe
    $trustedExe = Join-Path $stage "prukka.exe"
    Move-Item -LiteralPath $pendingExe -Destination $trustedExe
    if ((Get-Sha256 $trustedExe) -ne $exeDigest) {
        throw "executable identity changed after privileged activation"
    }

    $uninstallerDigest = Get-Sha256 $pendingUninstaller
    Set-AdminFileAcl $pendingUninstaller $true
    $trustedUninstaller = Join-Path $programData "PrukkaUninstall.ps1"
    Assert-NoReparseComponent $trustedUninstaller
    Move-Item -LiteralPath $pendingUninstaller -Destination $trustedUninstaller -Force
    if ((Get-Sha256 $trustedUninstaller) -ne
        $uninstallerDigest) {
        throw "uninstaller identity changed after privileged activation"
    }

    foreach ($name in @([Environment]::GetEnvironmentVariables().Keys)) {
        if ([string] $name -like "PRUKKA_*") {
            [Environment]::SetEnvironmentVariable([string] $name, $null, "Process")
        }
    }
    $env:ProgramData = $programData
    $tombstone = Join-Path $programData "PrukkaDevicesRemoved"
    Assert-NoReparseComponent $tombstone
    if (Test-Path -LiteralPath $tombstone) {
        Assert-AdminOwnedFile $tombstone
        Remove-Item -LiteralPath $tombstone -Force
    }
    & $trustedExe devices install
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
finally {
    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
}
'@
    $command = $command.Replace("__ARCHIVE__", $archiveToken).Replace(
        "__HASH__", $ExpectedHash.ToLowerInvariant())
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
    $hostExe = Join-Path ([Environment]::SystemDirectory) "WindowsPowerShell\v1.0\powershell.exe"
    Assert-NoReparseComponents $hostExe
    try {
        $process = Start-Process -FilePath $hostExe -Verb RunAs -Wait -PassThru -ArgumentList @(
            "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
            "-EncodedCommand", $encoded)
    }
    catch {
        Write-Warning "virtual-device elevation was cancelled or failed: $($_.Exception.Message)"
        return $false
    }
    if ($process.ExitCode -ne 0) {
        Write-Warning "privileged virtual-device setup exited with status $($process.ExitCode)"
        return $false
    }
    return $true
}

if (Test-IsElevated) {
    throw "run install.ps1 as a regular user; it requests UAC only for a verified device-install stage"
}

$repo = "ubyte-source/prukka"
$runtimeArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
if ($runtimeArch -eq "arm64") {
    throw "Windows ARM64 releases are not available until every bundled driver has a native ARM64 build"
}
if ($runtimeArch -ne "x64") {
    throw "unsupported Windows architecture: $runtimeArch"
}
$arch = "amd64"
$fixtureMode = Test-DeployFixtureMode
$localAppData = if ($fixtureMode -and $env:LOCALAPPDATA) {
    $env:LOCALAPPDATA
} else {
    [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
}
$tmpRoot = if ($fixtureMode -and $env:TEMP) { $env:TEMP } else { [IO.Path]::GetTempPath() }
$binDir = if ($env:PRUKKA_BIN_DIR) { $env:PRUKKA_BIN_DIR } else { Join-Path $localAppData "Prukka\bin" }
Assert-UserProfilePath $binDir "binary directory"
Assert-UserProfilePath $tmpRoot "temporary directory"

$customArchive = -not [string]::IsNullOrEmpty($env:PRUKKA_INSTALL_URL)
$url = if ($customArchive) { $env:PRUKKA_INSTALL_URL }
       else { "https://github.com/$repo/releases/latest/download/prukka_windows_$arch.zip" }

Write-Host "==> downloading prukka (windows/$arch)"
Write-Host "    $url"

$tmp = Join-Path $tmpRoot ("prukka-install-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
$exe = Join-Path $binDir "prukka.exe"
$aside = "$exe.install-old-$PID"
$uninstaller = Join-Path $binDir "prukka-uninstall.ps1"
$uninstallerAside = "$uninstaller.install-old-$PID"
$activated = $false
$uninstallerActivated = $false
$committed = $false
$hadOld = $false
$hadUninstaller = $false
$pathAdded = $false
$originalUserPath = $null

try {
    $zip = Join-Path $tmp "prukka.zip"
    Save-Download $url $zip

    $asset = "prukka_windows_$arch.zip"
    if ($customArchive) {
        $want = $env:PRUKKA_INSTALL_SHA256
        if ($env:PRUKKA_CHECKSUMS_URL) {
            throw "PRUKKA_CHECKSUMS_URL is disabled; pin custom archives with PRUKKA_INSTALL_SHA256"
        }
        if ($want -notmatch '^[0-9A-Fa-f]{64}$') {
            throw "PRUKKA_INSTALL_SHA256 is required with PRUKKA_INSTALL_URL"
        }
    } else {
        if ($env:PRUKKA_INSTALL_SHA256 -or $env:PRUKKA_CHECKSUMS_URL) {
            throw "custom checksum overrides require an explicit PRUKKA_INSTALL_URL"
        }
        $sumsUrl = "https://github.com/$repo/releases/latest/download/checksums.txt"
        $sums = Join-Path $tmp "checksums.txt"
        Save-Download $sumsUrl $sums
        $want = $null
        foreach ($line in [IO.File]::ReadAllLines($sums)) {
            $trimmed = $line.Trim()
            if ($trimmed.Length -eq 0) { continue }
            $parts = @($trimmed -split '\s+')
            if ($parts.Count -eq 2 -and $parts[1] -eq $asset) {
                $want = $parts[0]
                break
            }
        }
        if (-not $want) { throw "checksums.txt has no entry for $asset" }
        if ($want -notmatch '^[0-9A-Fa-f]{64}$') {
            throw "checksums.txt contains an invalid SHA-256 digest"
        }
    }

    $got = Get-FileSha256 $zip
    if ($got -ne $want.ToLowerInvariant()) {
        throw "checksum mismatch for ${asset}: got $got, want $want"
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($zip)
    try {
        $entries = @($archive.Entries | Where-Object {
            $_.FullName -eq "prukka.exe" -and $_.Name -eq "prukka.exe"
        })
        if ($entries.Count -ne 1) {
            throw "archive must contain exactly one top-level prukka.exe binary"
        }
        if ($entries[0].Length -le 0 -or $entries[0].Length -gt 200MB) {
            throw "prukka.exe has an invalid uncompressed size"
        }

        $uninstallerEntries = @($archive.Entries | Where-Object {
            $_.FullName -eq "deploy/uninstall.ps1" -and $_.Name -eq "uninstall.ps1"
        })
        if ($uninstallerEntries.Count -ne 1 -or $uninstallerEntries[0].Length -le 0 -or
            $uninstallerEntries[0].Length -gt 1MB) {
            throw "archive must contain exactly one bounded deploy/uninstall.ps1"
        }

        $inputStream = $entries[0].Open()
        $outputStream = [System.IO.File]::Create((Join-Path $tmp "prukka.exe"))
        try {
            $inputStream.CopyTo($outputStream)
        }
        finally {
            $outputStream.Dispose()
            $inputStream.Dispose()
        }

        $inputStream = $uninstallerEntries[0].Open()
        $outputStream = [System.IO.File]::Create((Join-Path $tmp "uninstall.ps1"))
        try {
            $inputStream.CopyTo($outputStream)
        }
        finally {
            $outputStream.Dispose()
            $inputStream.Dispose()
        }
    }
    finally {
        $archive.Dispose()
    }

    New-Item -ItemType Directory -Force -Path $binDir | Out-Null

    # A running prukka.exe locks its file against Copy-Item, but a rename
    # of the locked file still succeeds: move it aside, then install.
    if (Test-Path $exe) {
        Move-Item $exe $aside -Force
        $hadOld = $true
    }
    try {
        Copy-Item (Join-Path $tmp "prukka.exe") $exe -Force
        $activated = $true
    }
    catch {
        if ($hadOld) { Move-Item $aside $exe -Force }
        throw
    }

    if (Test-Path $uninstaller) {
        Move-Item $uninstaller $uninstallerAside -Force
        $hadUninstaller = $true
    }
    try {
        Copy-Item (Join-Path $tmp "uninstall.ps1") $uninstaller -Force
        $uninstallerActivated = $true
    }
    catch {
        if ($hadUninstaller) { Move-Item $uninstallerAside $uninstaller -Force }
        throw
    }

    Write-Host "==> installed $binDir\prukka.exe"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $onPath = ($userPath -split ";" | Where-Object { $_.TrimEnd("\") -eq $binDir.TrimEnd("\") }).Count -gt 0
    if (-not $onPath) {
        $originalUserPath = $userPath
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$binDir", "User")
        $pathAdded = $true
        Write-Host "==> added $binDir to your PATH (new terminals pick it up)"
    }

    Write-Host "==> installing dependencies (ffmpeg)"
    Invoke-Native $exe @("setup")

    # The service is a per-user scheduled task and needs no elevation;
    # only the device drivers do.
    Write-Host "==> registering the service"
    Invoke-Native $exe @("service", "install", "--now")

    # The new binary and service now form one working unit. Driver setup
    # may still need a manual retry, but must not roll this unit back.
    $committed = $true

    if ($customArchive) {
        Write-Warning "virtual-device setup skipped: custom archives are never executed elevated"
        $devicesInstalled = $false
    } else {
        Write-Host "==> installing the virtual devices (UAC)"
        $devicesInstalled = Invoke-PrivilegedDeviceInstall $zip $want.ToLowerInvariant()
    }

    Write-Host ""
    Write-Host "Prukka is ready."
    Write-Host ""
    if ($devicesInstalled) {
        Write-Host "  The daemon is running and the virtual devices are installed."
    } elseif ($customArchive) {
        Write-Host "  The daemon is running. Install an official release to set up virtual devices."
    } else {
        Write-Host "  The daemon is running. Rerun this installer to retry verified device setup."
        Write-Host "  Never elevate prukka.exe from a user-writable directory."
    }
    Write-Host ""
    Write-Host "  Speech lanes require a separately built local engine."
    Write-Host "  Configure providers.local.bin, then run: prukka doctor"
    Write-Host ""
    Write-Host "  Uninstall (add -Purge to remove configuration, state and logs too):"
    Write-Host "      & `"$uninstaller`""
    Write-Host ""
    Write-Host "Docs: https://github.com/$repo"
}
catch {
    $failure = $_

    if ($activated -and -not $committed) {
        if (-not $hadOld) {
            try { Invoke-Native $exe @("service", "remove") 2>$null | Out-Null } catch {}
        }

        Remove-Item $exe -Force -ErrorAction SilentlyContinue
        if ($hadOld) {
            try {
                Move-Item $aside $exe -Force
            }
            catch {
                throw "installation failed ($failure) and restoring $exe also failed: $_"
            }
        }

        if ($pathAdded) {
            [Environment]::SetEnvironmentVariable("Path", $originalUserPath, "User")
        }
    }

    if ($uninstallerActivated -and -not $committed) {
        Remove-Item $uninstaller -Force -ErrorAction SilentlyContinue
        if ($hadUninstaller) {
            try {
                Move-Item $uninstallerAside $uninstaller -Force
            }
            catch {
                throw "installation failed ($failure) and restoring $uninstaller also failed: $_"
            }
        }
    }

    throw $failure
}
finally {
    if ($committed) {
        Remove-OldImage $aside
        Remove-OldImage "$exe.old"
        Get-ChildItem -Path "$exe.install-old-*" -File -ErrorAction SilentlyContinue |
            ForEach-Object { Remove-OldImage $_.FullName }
        Remove-OldImage $uninstallerAside
        Get-ChildItem -Path "$uninstaller.install-old-*" -File -ErrorAction SilentlyContinue |
            ForEach-Object { Remove-OldImage $_.FullName }
    }
    if (Test-Path -LiteralPath $tmp) {
        Remove-CheckedTree $tmp
    }
}

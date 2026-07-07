# Prukka one-command installer for Windows (PowerShell):
#
#   irm https://prukka.ubyte.it/install.ps1 | iex
#
# Downloads the release binary, installs ffmpeg automatically (prukka setup)
# and explains the one command left (service install needs an elevated
# shell). Override $env:PRUKKA_INSTALL_URL for mirrors or local archives.
$ErrorActionPreference = "Stop"

$repo = "ubyte-source/prukka"
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$binDir = if ($env:PRUKKA_BIN_DIR) { $env:PRUKKA_BIN_DIR } else { Join-Path $env:LOCALAPPDATA "Prukka\bin" }

$url = if ($env:PRUKKA_INSTALL_URL) { $env:PRUKKA_INSTALL_URL }
       else { "https://github.com/$repo/releases/latest/download/prukka_windows_$arch.zip" }

Write-Host "==> downloading prukka (windows/$arch)"
Write-Host "    $url"

$tmp = Join-Path $env:TEMP ("prukka-install-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    $zip = Join-Path $tmp "prukka.zip"
    Invoke-WebRequest -Uri $url -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $tmp

    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    Copy-Item (Join-Path $tmp "prukka.exe") (Join-Path $binDir "prukka.exe") -Force

    Write-Host "==> installed $binDir\prukka.exe"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$binDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$binDir", "User")
        Write-Host "==> added $binDir to your PATH (new terminals pick it up)"
    }

    Write-Host "==> installing dependencies (ffmpeg)"
    & (Join-Path $binDir "prukka.exe") setup

    Write-Host ""
    Write-Host "Prukka is ready."
    Write-Host ""
    Write-Host "  Start now (foreground, opens the dashboard):"
    Write-Host "      prukka up"
    Write-Host ""
    Write-Host "  Or install as a Windows service (run in an elevated shell):"
    Write-Host "      prukka service install --now"
    Write-Host ""
    Write-Host "  Store your OpenRouter key (hidden prompt, goes to Credential Manager):"
    Write-Host "      prukka key set openrouter"
    Write-Host ""
    Write-Host "Docs: https://github.com/$repo"
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

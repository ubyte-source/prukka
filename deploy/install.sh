#!/bin/sh
# Prukka one-command installer for macOS and Linux:
#
#   curl -fsSL https://prukka.ubyte.it/install.sh | sh
#
# Downloads the release binary for this platform, installs the ffmpeg
# dependency automatically (prukka setup), registers the system service and
# opens the dashboard. Override PRUKKA_INSTALL_URL to install from a mirror
# or a local archive (used by the repo's own tests).
set -eu

REPO="ubyte-source/prukka"
BIN_DIR="${PRUKKA_BIN_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)

case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

case "$os" in
  darwin | linux) ;;
  *) echo "unsupported OS: $os (Windows: use install.ps1)" >&2; exit 1 ;;
esac

url="${PRUKKA_INSTALL_URL:-https://github.com/$REPO/releases/latest/download/prukka_${os}_${arch}.tar.gz}"

echo "==> downloading prukka (${os}/${arch})"
echo "    $url"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$url" -o "$tmp/prukka.tar.gz"
tar -xzf "$tmp/prukka.tar.gz" -C "$tmp"

mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/prukka" "$BIN_DIR/prukka"

echo "==> installed $BIN_DIR/prukka"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH" ;;
esac

echo "==> installing dependencies (ffmpeg)"
"$BIN_DIR/prukka" setup

echo "==> environment check"
"$BIN_DIR/prukka" doctor || true

cat <<EOF

Prukka is ready.

  Start now (foreground, opens the dashboard):
      $BIN_DIR/prukka up

  Or install as a service that survives reboots:
      sudo $BIN_DIR/prukka service install --now

  Store your OpenRouter key (hidden prompt, goes to the OS keychain):
      $BIN_DIR/prukka key set openrouter

Docs: https://github.com/$REPO
EOF

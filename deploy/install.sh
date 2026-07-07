#!/bin/sh
# Prukka one-command installer for macOS and Linux:
#
#   curl -fsSL https://prukka.ubyte.it/install.sh | sh
#
# Downloads the release binary for this platform, verifies it against the
# release checksums, installs the ffmpeg dependency automatically (prukka
# setup), registers the per-user service, installs the virtual devices and
# opens the dashboard. Override PRUKKA_INSTALL_URL to install from a
# mirror or a local archive together with PRUKKA_INSTALL_SHA256.
set -eu

# privileged runs a command as root, through sudo when needed; without
# root or sudo it reports failure so the caller can print the command.
privileged() {
  if [ "${test_mode:-0}" -eq 1 ]; then
    "$@"
  elif [ "$(/usr/bin/id -u)" -eq 0 ]; then
    /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"
  elif [ -x /usr/bin/sudo ]; then
    /usr/bin/sudo /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"
  else
    return 1
  fi
}

sha256_file() {
  file=$1
  if [ -x /usr/bin/sha256sum ]; then
    /usr/bin/sha256sum "$file" | /usr/bin/awk '{ print $1 }'
  elif [ -x /usr/bin/shasum ]; then
    /usr/bin/shasum -a 256 "$file" | /usr/bin/awk '{ print $1 }'
  else
    echo "SHA-256 tool not found (need sha256sum or shasum)" >&2
    return 1
  fi
}

privileged_sha256_file() {
  file=$1
  output=$(privileged /usr/bin/sha256sum "$file" 2>/dev/null || true)
  if [ -z "$output" ]; then
    output=$(privileged /usr/bin/shasum -a 256 "$file" 2>/dev/null || true)
  fi
  digest=$(printf '%s\n' "$output" | /usr/bin/awk 'NR == 1 { print $1 }')
  [ "${#digest}" -eq 64 ] || return 1
  case "$digest" in *[!0-9A-Fa-f]*) return 1 ;; esac
  printf '%s\n' "$digest" | /usr/bin/tr '[:upper:]' '[:lower:]'
}

no_symlink_ancestors() {
  path=$1
  while [ "${path%/}" != "$path" ]; do path=${path%/}; done
  case "$path" in
    /*) ;;
    *) return 1 ;;
  esac
  case "$path" in *//* | */../* | */.. | */./* | */.) return 1 ;; esac

  parent=${path%/*}
  [ -n "$parent" ] || parent=/
  rest=${parent#/}
  current=
  while [ -n "$rest" ]; do
    component=${rest%%/*}
    if [ "$component" = "$rest" ]; then rest=; else rest=${rest#*/}; fi
    [ -n "$component" ] || return 1
    current="$current/$component"
    [ ! -L "$current" ] || return 1
    [ -e "$current" ] || break
  done
}

safe_profile_dir() {
  path=$1
  home_real=$(CDPATH= cd -P -- "$HOME" 2>/dev/null && pwd -P) || return 1
  case "$path" in
    "$HOME"/*) check="$home_real/${path#"$HOME"/}" ;;
    "$home_real"/*) check=$path ;;
    *) return 1 ;;
  esac
  no_symlink_ancestors "$check" || return 1
  [ ! -L "$path" ] || return 1
  [ ! -e "$path" ] || [ -d "$path" ]
}

# Copy the image into a root-created private directory, verify the copy, then
# atomically publish and execute it. No executable below the user's profile
# crosses the privilege boundary directly.
privileged_prukka() {
  archive=$1
  expected=$2
  shift 2

  [ -f "$archive" ] && [ ! -L "$archive" ] || return 1
  current=$(sha256_file "$archive") || return 1
  [ "$current" = "$expected" ] || return 1

  if [ "${PRUKKA_PRIVILEGED_STAGE_ROOT+x}" = x ]; then
    root=$PRUKKA_PRIVILEGED_STAGE_ROOT
  elif [ "${os:-}" = darwin ]; then
    root=/private/var/tmp
  else
    root=/var/tmp
  fi
  root_stage=$(privileged mktemp -d "$root/prukka-privileged.XXXXXX") || return 1
  case "$root_stage" in
    "$root"/prukka-privileged.*) ;;
    *)
      echo "refusing unexpected privileged staging path: $root_stage" >&2
      return 1
      ;;
  esac
  privileged_stage=$root_stage

  if ! privileged chmod 0700 "$root_stage" ||
     ! privileged install -m 0600 "$archive" "$root_stage/.release.new"; then
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi

  copied=$(privileged_sha256_file "$root_stage/.release.new" || true)
  current=$(sha256_file "$archive" || true)
  if [ "$copied" != "$expected" ] || [ "$current" != "$expected" ]; then
    echo "privileged staging identity changed during copy; refusing to execute" >&2
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi

  if ! privileged mv -f "$root_stage/.release.new" "$root_stage/release.tar.gz"; then
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi

  copied=$(privileged_sha256_file "$root_stage/release.tar.gz" || true)
  if [ "$copied" != "$expected" ]; then
    echo "privileged archive identity changed after activation; refusing to execute" >&2
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi

  # Extract the exact regular entry into a private subdirectory in one tar
  # process, so POSIX pipeline status cannot hide a partial extraction.
  if ! privileged mkdir -m 0700 "$root_stage/extract" ||
     ! privileged /usr/bin/tar -xzf "$root_stage/release.tar.gz" \
       -C "$root_stage/extract" prukka; then
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi
  if ! privileged chmod 0700 "$root_stage/extract/prukka"; then
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi
  binary_digest=$(privileged_sha256_file "$root_stage/extract/prukka" || true)
  if [ -z "$binary_digest" ] ||
     [ "$binary_digest" = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ]; then
    echo "privileged archive contains no executable payload" >&2
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi
  if ! privileged mv -f "$root_stage/extract/prukka" "$root_stage/prukka" ||
     ! privileged rmdir "$root_stage/extract"; then
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi
  copied=$(privileged_sha256_file "$root_stage/prukka" || true)
  if [ "$copied" != "$binary_digest" ]; then
    echo "privileged executable identity changed after activation; refusing to execute" >&2
    privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
    privileged_stage=
    return 1
  fi

  status=0
  privileged "$root_stage/prukka" "$@" || status=$?
  privileged rm -rf "$root_stage" >/dev/null 2>&1 || true
  privileged_stage=
  return "$status"
}

# Everything runs from main() so a truncated `curl | sh` stream can never
# execute half a script.
main() {
  REPO="ubyte-source/prukka"
  BIN_DIR="${PRUKKA_BIN_DIR:-$HOME/.local/bin}"
  LEGACY_LINK="${PRUKKA_LEGACY_LINK:-/usr/local/bin/prukka}"

  tmp=""
  stage=""
  backup=""
  uninstall_stage=""
  uninstall_backup=""
  privileged_stage=""
  activated=0
  uninstall_activated=0
  committed=0
  had_old=0
  had_uninstaller=0

  cleanup() {
    status=$?
    trap - EXIT

    [ -z "$stage" ] || rm -f "$stage" || true
    [ -z "$uninstall_stage" ] || rm -f "$uninstall_stage" || true

    if [ "$activated" -eq 1 ] && [ "$committed" -eq 0 ]; then
      if [ "$had_old" -eq 1 ]; then
        mv -f "$backup" "$BIN_DIR/prukka" || true
      else
        rm -f "$BIN_DIR/prukka" || true
      fi
    fi
    if [ "$uninstall_activated" -eq 1 ] && [ "$committed" -eq 0 ]; then
      if [ "$had_uninstaller" -eq 1 ]; then
        mv -f "$uninstall_backup" "$BIN_DIR/prukka-uninstall" || true
      else
        rm -f "$BIN_DIR/prukka-uninstall" || true
      fi
    fi

    [ -z "$backup" ] || rm -f "$backup" || true
    [ -z "$uninstall_backup" ] || rm -f "$uninstall_backup" || true
    [ -z "$privileged_stage" ] || privileged rm -rf "$privileged_stage" >/dev/null 2>&1 || true
    [ -z "$tmp" ] || rm -rf "$tmp" || true
    exit "$status"
  }

  # Prukka runs as a per-user service: installed as root, the binary would
  # land in /root and the service registration would refuse to run.
  if [ "$(id -u)" -eq 0 ]; then
    echo "run this installer as your regular user — it asks for sudo only where needed" >&2
    exit 1
  fi

  test_mode=0
  if [ -n "${PRUKKA_DEPLOY_TEST_MODE:-}" ]; then
    if [ "$PRUKKA_DEPLOY_TEST_MODE" != "prukka-deploy-fixtures-v1" ] ||
       [ -z "${PRUKKA_TEST_ROOT:-}" ]; then
      echo "invalid deploy test mode" >&2
      exit 1
    fi
    home_real=$(CDPATH= cd -P -- "$HOME" 2>/dev/null && pwd -P) || exit 1
    test_root_input=${PRUKKA_TEST_ROOT%/}
    test_root=$(CDPATH= cd -P -- "$test_root_input" 2>/dev/null && pwd -P) || {
      echo "deploy test root must already exist" >&2
      exit 1
    }
    case "$test_root" in
      "$home_real" | "$home_real"/*) ;;
      *) echo "deploy test root must remain below HOME" >&2; exit 1 ;;
    esac
    test_mode=1
    PRUKKA_PRIVILEGED_STAGE_ROOT="$test_root/privileged"
    privileged mkdir -p "$PRUKKA_PRIVILEGED_STAGE_ROOT" || exit 1
  elif [ "${PRUKKA_LEGACY_LINK+x}" = x ] ||
       [ "${PRUKKA_PRIVILEGED_STAGE_ROOT+x}" = x ] ||
       [ "${PRUKKA_TEST_ROOT+x}" = x ]; then
    echo "fixture path overrides require PRUKKA_DEPLOY_TEST_MODE=prukka-deploy-fixtures-v1" >&2
    exit 1
  fi

  if [ "${PRUKKA_LEGACY_LINK+x}" = x ]; then
    [ "$test_mode" -eq 1 ] || exit 1
    case "$PRUKKA_LEGACY_LINK" in
      "$test_root_input"/* | "$test_root"/*) LEGACY_LINK=$PRUKKA_LEGACY_LINK ;;
      *) echo "test legacy link escapes the deploy test root" >&2; exit 1 ;;
    esac
  fi

  safe_profile_dir "$BIN_DIR" || {
    echo "refusing unsafe install directory: $BIN_DIR" >&2
    exit 1
  }

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)

  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
  esac

  # A shell running under Rosetta reports x86_64 on Apple silicon; the
  # native binary is the right one.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
     [ "$(sysctl -in sysctl.proc_translated 2>/dev/null)" = 1 ]; then
    arch=arm64
  fi

  case "$os" in
    darwin | linux) ;;
    *) echo "unsupported OS: $os (Windows: use install.ps1)" >&2; exit 1 ;;
  esac

  base="https://github.com/$REPO/releases/latest/download"
  url="${PRUKKA_INSTALL_URL:-$base/prukka_${os}_${arch}.tar.gz}"

  echo "==> downloading prukka (${os}/${arch})"
  echo "    $url"

  tmp=$(mktemp -d)
  trap cleanup EXIT

  [ -x /usr/bin/curl ] || { echo "trusted /usr/bin/curl is required" >&2; exit 1; }
  /usr/bin/curl -fsSL "$url" -o "$tmp/prukka.tar.gz"

  # A custom mirror must provide an explicit archive digest. Its contents stay
  # user-mode because a caller-supplied URL plus digest is not an authenticity
  # proof for privileged execution.
  if [ -z "${PRUKKA_INSTALL_URL:-}" ]; then
    echo "==> verifying checksum"
    /usr/bin/curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

    want=$(/usr/bin/awk -v name="prukka_${os}_${arch}.tar.gz" \
      '$2 == name { print $1 }' "$tmp/checksums.txt")
    if [ -z "$want" ]; then
      echo "checksum for prukka_${os}_${arch}.tar.gz missing from checksums.txt" >&2
      exit 1
    fi

  else
    want=${PRUKKA_INSTALL_SHA256:-}
    if [ -z "$want" ]; then
      echo "PRUKKA_INSTALL_SHA256 is required with PRUKKA_INSTALL_URL" >&2
      exit 1
    fi
  fi

  want=$(printf '%s\n' "$want" | /usr/bin/tr '[:upper:]' '[:lower:]')
  if [ "${#want}" -ne 64 ]; then
    echo "invalid SHA-256 digest" >&2
    exit 1
  fi
  case "$want" in *[!0-9a-f]*) echo "invalid SHA-256 digest" >&2; exit 1 ;; esac
  got=$(sha256_file "$tmp/prukka.tar.gz")
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch: got $got, want $want" >&2
    exit 1
  fi

  binary_entries=$(/usr/bin/tar -tzf "$tmp/prukka.tar.gz" |
    /usr/bin/awk '$0 == "prukka" { n++ } END { print n + 0 }')
  if [ "$binary_entries" -ne 1 ]; then
    echo "archive must contain exactly one top-level prukka binary" >&2
    exit 1
  fi
  binary_type=$(LC_ALL=C /usr/bin/tar -tvzf "$tmp/prukka.tar.gz" prukka |
    /usr/bin/awk 'NR == 1 { print substr($1, 1, 1) }')
  if [ "$binary_type" != - ]; then
    echo "archive prukka entry must be a regular file" >&2
    exit 1
  fi
  /usr/bin/tar -xOzf "$tmp/prukka.tar.gz" prukka > "$tmp/prukka"
  if [ ! -s "$tmp/prukka" ]; then
    echo "archive contains an empty prukka binary" >&2
    exit 1
  fi
  uninstall_entries=$(/usr/bin/tar -tzf "$tmp/prukka.tar.gz" |
    /usr/bin/awk '$0 == "deploy/uninstall.sh" { n++ } END { print n + 0 }')
  if [ "$uninstall_entries" -ne 1 ]; then
    echo "archive must contain exactly one deploy/uninstall.sh" >&2
    exit 1
  fi
  uninstall_type=$(LC_ALL=C /usr/bin/tar -tvzf "$tmp/prukka.tar.gz" deploy/uninstall.sh |
    /usr/bin/awk 'NR == 1 { print substr($1, 1, 1) }')
  if [ "$uninstall_type" != - ]; then
    echo "archive deploy/uninstall.sh entry must be a regular file" >&2
    exit 1
  fi
  /usr/bin/tar -xOzf "$tmp/prukka.tar.gz" deploy/uninstall.sh > "$tmp/uninstall.sh"
  if [ ! -s "$tmp/uninstall.sh" ]; then
    echo "archive contains an empty uninstaller" >&2
    exit 1
  fi

  mkdir -p "$BIN_DIR"
  safe_profile_dir "$BIN_DIR" || {
    echo "install directory changed identity while it was created: $BIN_DIR" >&2
    exit 1
  }
  stage="$BIN_DIR/.prukka.new.$$"
  backup="$BIN_DIR/.prukka.old.$$"
  uninstall_stage="$BIN_DIR/.prukka-uninstall.new.$$"
  uninstall_backup="$BIN_DIR/.prukka-uninstall.old.$$"
  install -m 0755 "$tmp/prukka" "$stage"
  install -m 0755 "$tmp/uninstall.sh" "$uninstall_stage"

  if [ -e "$BIN_DIR/prukka" ]; then
    ln "$BIN_DIR/prukka" "$backup"
    had_old=1
  fi
  if [ -e "$BIN_DIR/prukka-uninstall" ]; then
    cp -p "$BIN_DIR/prukka-uninstall" "$uninstall_backup"
    had_uninstaller=1
  fi

  mv -f "$stage" "$BIN_DIR/prukka"
  stage=""
  activated=1
  mv -f "$uninstall_stage" "$BIN_DIR/prukka-uninstall"
  uninstall_stage=""
  uninstall_activated=1

  echo "==> installed $BIN_DIR/prukka"

  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) [ -x "$LEGACY_LINK" ] || echo "note: add $BIN_DIR to your PATH" ;;
  esac

  echo "==> installing dependencies (ffmpeg)"
  "$BIN_DIR/prukka" setup

  # Never leave a root-visible command pointing at a user-writable image:
  # that would turn `sudo prukka ...` into a privilege boundary bypass.
  # Remove only the legacy link created by older Prukka installers.
  if [ -L "$LEGACY_LINK" ] &&
     [ "$(readlink "$LEGACY_LINK")" = "$BIN_DIR/prukka" ]; then
    echo "==> removing legacy $LEGACY_LINK link (may prompt for sudo)"
    if ! no_symlink_ancestors "$LEGACY_LINK" || ! privileged rm -f "$LEGACY_LINK"; then
      echo "cannot remove unsafe legacy link $LEGACY_LINK; remove it as root and retry" >&2
      exit 1
    fi
  fi

  echo "==> registering the service"
  if "$BIN_DIR/prukka" service install --now; then
    if [ -n "${PRUKKA_INSTALL_URL:-}" ] && [ "$test_mode" -ne 1 ]; then
      next_step="The daemon is running. Virtual-device setup was skipped because custom archives are never executed with root privileges.

  Install an official release to set up virtual devices."
    elif privileged_prukka "$tmp/prukka.tar.gz" "$want" devices install; then
      next_step="The daemon is running and the virtual devices are installed."
    else
      next_step="The daemon is running, but virtual-device setup did not complete.

  To retry virtual-device setup safely, rerun this verified installer.
  Never run a binary from $BIN_DIR with sudo."
    fi
  else
    next_step="Finish the per-user service if needed:
      \"$BIN_DIR/prukka\" service install --now

  To retry virtual-device setup safely, rerun this verified installer.
  Never run a binary from $BIN_DIR with sudo.

  Or start in the foreground instead (opens the dashboard):
      \"$BIN_DIR/prukka\" up"
  fi

  committed=1
  rm -f "$backup"
  backup=""
  rm -f "$uninstall_backup"
  uninstall_backup=""

  echo "==> environment check"
  "$BIN_DIR/prukka" doctor || true

  cat <<EOF

Prukka is ready.

  $next_step

  Speech lanes require a separately built local engine. Configure
  providers.local.bin, then run:
      "$BIN_DIR/prukka" doctor

  Uninstall (add --purge to remove configuration, state and logs too):
      "$BIN_DIR/prukka-uninstall"

Docs: https://github.com/$REPO
EOF
}

main "$@"

#!/bin/sh
# Remove Prukka on macOS or Linux. By default user configuration, state,
# logs and legacy provider credentials are retained; --purge removes those too.
set -eu

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

owned_dir() {
  path=$1
  while [ "${path%/}" != "$path" ]; do path=${path%/}; done
  [ -n "$path" ] && [ "$path" != / ] || return 1

  case "${path##*/}" in
    prukka | Prukka) return 0 ;;
    *) return 1 ;;
  esac
}

safe_user_path() {
  path=$1
  kind=$2
  while [ "${path%/}" != "$path" ]; do path=${path%/}; done

  case "$path" in
    /*) ;;
    *) return 1 ;;
  esac
  case "$path" in
    *//* | */../* | */.. | */./* | */.) return 1 ;;
  esac

  home_input=${HOME%/}
  home_real=$(CDPATH= cd -P -- "$HOME" 2>/dev/null && pwd -P) || return 1
  case "$path" in
    "$home_input"/*) check="$home_real/${path#"$home_input"/}" ;;
    "$home_real"/*) check=$path ;;
    *) return 1 ;;
  esac

  rest=${check#"$home_real"/}
  current=$home_real
  while [ -n "$rest" ]; do
    component=${rest%%/*}
    if [ "$component" = "$rest" ]; then
      rest=
    else
      rest=${rest#*/}
    fi
    [ -n "$component" ] || return 1
    current="$current/$component"
    [ ! -L "$current" ] || return 1
    [ -e "$current" ] || break
  done

  [ ! -L "$path" ] || return 1
  if [ -e "$path" ]; then
    case "$kind" in
      dir) [ -d "$path" ] || return 1 ;;
      file) [ -f "$path" ] || return 1 ;;
      *) return 1 ;;
    esac
  fi
}

no_symlink_components() {
  path=$1
  while [ "${path%/}" != "$path" ]; do path=${path%/}; done
  if [ "${test_mode:-0}" -eq 1 ]; then
    case "$path" in
      "$test_root_input") path=$test_root ;;
      "$test_root_input"/*) path="$test_root/${path#"$test_root_input"/}" ;;
    esac
  fi
  case "$path" in /*) ;; *) return 1 ;; esac
  case "$path" in *//* | */../* | */.. | */./* | */.) return 1 ;; esac

  rest=${path#/}
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

no_symlink_ancestors() {
  path=$1
  parent=${path%/*}
  [ -n "$parent" ] || parent=/
  no_symlink_components "$parent"
}

# Anchor destructive user-data removal to an already-open physical parent.
# A concurrent leaf swap can then remove only an object in that directory;
# replacing an ancestor cannot redirect the operation outside the profile.
remove_user_tree() {
  path=$1
  [ -e "$path" ] || [ -L "$path" ] || return 0
  safe_user_path "$path" dir || return 1
  parent=${path%/*}
  leaf=${path##*/}
  parent_real=$(CDPATH= cd -P -- "$parent" 2>/dev/null && pwd -P) || return 1
  quarantine=".prukka-remove-$$-$leaf"
  (
    CDPATH= cd -P -- "$parent" || exit 1
    [ "$(pwd -P)" = "$parent_real" ] || exit 1
    [ ! -e "$quarantine" ] && [ ! -L "$quarantine" ] || exit 1
    [ -d "$leaf" ] && [ ! -L "$leaf" ] || exit 1
    mv "$leaf" "$quarantine" || exit 1
    rm -rf "$quarantine"
  )
}

remove_user_file() {
  path=$1
  [ -e "$path" ] || [ -L "$path" ] || return 0
  safe_user_path "$path" file || return 1
  parent=${path%/*}
  leaf=${path##*/}
  parent_real=$(CDPATH= cd -P -- "$parent" 2>/dev/null && pwd -P) || return 1
  (
    CDPATH= cd -P -- "$parent" || exit 1
    [ "$(pwd -P)" = "$parent_real" ] || exit 1
    [ -f "$leaf" ] && [ ! -L "$leaf" ] || exit 1
    rm -f "$leaf"
  )
}

remove_leaf_from_dir() {
  dir=$1
  leaf=$2
  case "$leaf" in "" | */* | . | ..) return 1 ;; esac
  dir_real=$(CDPATH= cd -P -- "$dir" 2>/dev/null && pwd -P) || return 0
  (
    CDPATH= cd -P -- "$dir" || exit 1
    [ "$(pwd -P)" = "$dir_real" ] || exit 1
    [ ! -d "$leaf" ] || exit 1
    rm -f "$leaf"
  )
}

remove_user_dir_if_empty() {
  path=$1
  [ -d "$path" ] || return 0
  safe_user_path "$path" dir || return 1
  parent=${path%/*}
  leaf=${path##*/}
  parent_real=$(CDPATH= cd -P -- "$parent" 2>/dev/null && pwd -P) || return 1
  (
    CDPATH= cd -P -- "$parent" || exit 1
    [ "$(pwd -P)" = "$parent_real" ] || exit 1
    [ -d "$leaf" ] && [ ! -L "$leaf" ] || exit 1
    rmdir "$leaf" 2>/dev/null || true
  )
}

safe_runtime_dir() {
  path=$1
  owned_dir "$path" || return 1
  home_real=$(CDPATH= cd -P -- "$HOME" 2>/dev/null && pwd -P) || return 1
  case "$path" in
    "$HOME"/*) check="$home_real/${path#"$HOME"/}" ;;
    *) check=$path ;;
  esac
  no_symlink_components "$check" || return 1
  uid=$(id -u)
  case "$path" in
    /run/user/"$uid"/prukka | "$HOME"/*/prukka | "$home_real"/*/prukka) return 0 ;;
    *) return 1 ;;
  esac
}

fallback_service_remove() {
  failed=0
  case "$os" in
    darwin)
      target="gui/$(id -u)/io.prukka.daemon"
      plist="$HOME/Library/LaunchAgents/io.prukka.daemon.plist"
      if command -v launchctl >/dev/null 2>&1; then
        launchctl bootout "$target" >/dev/null 2>&1 || true
      else
        echo "residue: launchd job $target could not be queried without launchctl" >&2
        failed=1
      fi
      rm -f "$plist" || failed=1
      if command -v launchctl >/dev/null 2>&1 && launchctl print "$target" >/dev/null 2>&1; then
        echo "residue: launchd job $target is still loaded" >&2
        failed=1
      fi
      ;;
    linux)
      unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
      if command -v systemctl >/dev/null 2>&1; then
        systemctl --user disable --now prukka.service >/dev/null 2>&1 || true
      else
        echo "residue: systemd user unit prukka.service could not be queried without systemctl" >&2
        failed=1
      fi
      if safe_user_path "$unit_dir" dir &&
         safe_user_path "$unit_dir/default.target.wants" dir; then
        remove_leaf_from_dir "$unit_dir" prukka.service || failed=1
        remove_leaf_from_dir "$unit_dir/default.target.wants" prukka.service || failed=1
      else
        echo "residue: refusing unsafe systemd user-unit path $unit_dir" >&2
        failed=1
      fi
      if command -v systemctl >/dev/null 2>&1; then
        systemctl --user daemon-reload >/dev/null 2>&1 || true
        if systemctl --user is-active --quiet prukka.service >/dev/null 2>&1; then
          echo "residue: systemd user unit prukka.service is still active" >&2
          return 1
        fi
      fi
      ;;
  esac

  [ "$failed" -eq 0 ]
}

fallback_macos_devices() {
  failed=0
  audio_changed=0
  for path in \
    "/Library/Audio/Plug-Ins/HAL/PrukkaMic.driver" \
    "/Library/Audio/Plug-Ins/HAL/PrukkaSpeaker.driver"; do
    if [ -e "$path" ] || [ -L "$path" ]; then audio_changed=1; fi
    if no_symlink_components "$path"; then
      privileged rm -rf "$path" >/dev/null 2>&1 || true
    fi
    if [ -e "$path" ] || [ -L "$path" ]; then
      echo "residue: $path" >&2
      failed=1
    fi
  done

  for path in \
    "/Applications/Prukka Camera.app" \
    "/Library/Application Support/Prukka/devices"; do
    if no_symlink_components "$path"; then
      privileged rm -rf "$path" >/dev/null 2>&1 || true
    fi
    if [ -e "$path" ] || [ -L "$path" ]; then
      echo "residue: $path" >&2
      failed=1
    fi
  done
  privileged rmdir "/Library/Application Support/Prukka" >/dev/null 2>&1 || true

  if [ "$audio_changed" -eq 1 ]; then
    privileged launchctl kickstart -kp system/com.apple.audio.coreaudiod >/dev/null 2>&1 ||
      privileged killall coreaudiod >/dev/null 2>&1 || {
        echo "residue: coreaudiod must be restarted or macOS rebooted" >&2
        failed=1
      }
  fi

  [ "$failed" -eq 0 ]
}

fallback_linux_devices() {
  failed=0
  kernels=
  if [ "${PRUKKA_LINUX_MODULE_ROOT+x}" = x ]; then
    module_root=$PRUKKA_LINUX_MODULE_ROOT
  elif [ -d /usr/lib/modules ]; then
    # usrmerge commonly makes /lib a symlink; use the physical root so the
    # ancestor-symlink rejection remains strict without breaking cleanup.
    module_root=/usr/lib/modules
  else
    module_root=/lib/modules
  fi
  proc_modules=${PRUKKA_LINUX_PROC_MODULES:-/proc/modules}
  modules_load_conf=${PRUKKA_LINUX_MODULES_LOAD_CONF:-/etc/modules-load.d/prukka.conf}
  device_state_dir=${PRUKKA_DEVICE_STATE_DIR:-/var/lib/prukka/devices}
  for path in "$module_root" "$proc_modules" "$modules_load_conf" "$device_state_dir"; do
    if ! no_symlink_components "$path"; then
      echo "residue: refusing privileged cleanup through symlinked path $path" >&2
      return 1
    fi
  done
  for module in prukka_webcam snd_prukka_speaker snd_prukka_mic; do
    if [ -r "$proc_modules" ] && grep -q "^$module " "$proc_modules"; then
      privileged modprobe -r "$module" >/dev/null 2>&1 || true
      if grep -q "^$module " "$proc_modules"; then
        echo "residue: loaded kernel module $module" >&2
        failed=1
      fi
    fi

    for file in "$module_root"/*/extra/"$module.ko"; do
      [ -e "$file" ] || continue
      kernel=${file#"$module_root"/}
      kernel=${kernel%%/*}
      case "$kernel" in
        "" | *[!0-9A-Za-z._+-]*)
          echo "residue: unsafe kernel module path $file" >&2
          failed=1
          continue
          ;;
      esac
      if ! no_symlink_components "$file"; then
        echo "residue: refusing symlinked kernel module path $file" >&2
        failed=1
        continue
      fi
      privileged rm -f "$file" >/dev/null 2>&1 || true
      if [ -e "$file" ]; then
        echo "residue: $file" >&2
        failed=1
      else
        case " $kernels " in
          *" $kernel "*) ;;
          *) kernels="$kernels $kernel" ;;
        esac
      fi
    done
  done

  privileged rm -f "$modules_load_conf" >/dev/null 2>&1 || true
  if [ -e "$modules_load_conf" ]; then
    echo "residue: $modules_load_conf" >&2
    failed=1
  fi
  privileged rm -rf "$device_state_dir" >/dev/null 2>&1 || true
  if [ -e "$device_state_dir" ] || [ -L "$device_state_dir" ]; then
    echo "residue: $device_state_dir" >&2
    failed=1
  fi
  if no_symlink_components "${device_state_dir%/*}"; then
    privileged rmdir "${device_state_dir%/*}" >/dev/null 2>&1 || true
  fi

  for kernel in $kernels; do
    if ! privileged depmod -a "$kernel" >/dev/null 2>&1; then
      echo "residue: depmod metadata for kernel $kernel needs regeneration" >&2
      failed=1
    fi
  done

  [ "$failed" -eq 0 ]
}

fallback_devices_remove() {
  case "$os" in
    darwin) fallback_macos_devices ;;
    linux) fallback_linux_devices ;;
  esac
}

fallback_legacy_credentials_remove() {
  failed=0
  case "$os" in
    darwin)
      if ! command -v security >/dev/null 2>&1; then
        echo "residue: legacy Keychain service prukka accounts openrouter and cartesia" >&2
        return 1
      fi
      for account in openrouter cartesia; do
        security delete-generic-password -s prukka -a "$account" >/dev/null 2>&1 || true
        if security find-generic-password -s prukka -a "$account" >/dev/null 2>&1; then
          echo "residue: legacy Keychain service prukka account $account" >&2
          failed=1
        fi
      done
      ;;
    linux)
      if ! command -v secret-tool >/dev/null 2>&1; then
        echo "residue: legacy Secret Service entries service=prukka username=openrouter/cartesia" >&2
        return 1
      fi
      for account in openrouter cartesia; do
        secret-tool clear service prukka username "$account" >/dev/null 2>&1 || true
        if secret-tool lookup service prukka username "$account" 2>/dev/null | grep -q .; then
          echo "residue: legacy Secret Service entry service=prukka username=$account" >&2
          failed=1
        fi
      done
      ;;
  esac

  [ "$failed" -eq 0 ]
}

main() {
  purge=0
  case "${1:-}" in
    "") ;;
    --purge) purge=1 ;;
    -h | --help)
      echo "usage: uninstall.sh [--purge]"
      exit 0
      ;;
    *) echo "usage: uninstall.sh [--purge]" >&2; exit 2 ;;
  esac

  if [ "$(id -u)" -eq 0 ]; then
    echo "run this uninstaller as the user who installed Prukka" >&2
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
  elif [ "${PRUKKA_TEST_ROOT+x}" = x ] || [ "${PRUKKA_BIN+x}" = x ] ||
       [ "${PRUKKA_LEGACY_LINK+x}" = x ] || [ "${PRUKKA_STATE+x}" = x ] ||
       [ "${PRUKKA_CONFIG+x}" = x ] || [ "${PRUKKA_LINUX_MODULE_ROOT+x}" = x ] ||
       [ "${PRUKKA_LINUX_PROC_MODULES+x}" = x ] ||
       [ "${PRUKKA_LINUX_MODULES_LOAD_CONF+x}" = x ] ||
       [ "${PRUKKA_DEVICE_STATE_DIR+x}" = x ]; then
    echo "fixture path overrides require PRUKKA_DEPLOY_TEST_MODE=prukka-deploy-fixtures-v1" >&2
    exit 1
  fi

  if [ "$test_mode" -eq 1 ]; then
    for path in "${PRUKKA_BIN:-}" "${PRUKKA_LEGACY_LINK:-}" "${PRUKKA_STATE:-}" \
      "${PRUKKA_CONFIG:-}" "${PRUKKA_LINUX_MODULE_ROOT:-}" \
      "${PRUKKA_LINUX_PROC_MODULES:-}" "${PRUKKA_LINUX_MODULES_LOAD_CONF:-}" \
      "${PRUKKA_DEVICE_STATE_DIR:-}"; do
      [ -z "$path" ] && continue
      case "$path" in
        "$test_root_input" | "$test_root_input"/* | "$test_root" | "$test_root"/*) ;;
        *) echo "fixture path escapes the deploy test root: $path" >&2; exit 1 ;;
      esac
    done
  fi

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  bin_dir=${PRUKKA_BIN_DIR:-"$HOME/.local/bin"}
  safe_user_path "$bin_dir" dir || {
    echo "refusing unsafe binary directory: $bin_dir" >&2
    exit 1
  }
  if [ "${PRUKKA_BIN+x}" = x ]; then
    expected_prukka=$PRUKKA_BIN
  else
    expected_prukka="$bin_dir/prukka"
  fi
  prukka=$expected_prukka
  have_prukka=1
  if ! safe_user_path "$prukka" file || [ ! -x "$prukka" ]; then
    have_prukka=0
    echo "Prukka's executable is unavailable; using the platform cleanup fallback" >&2
  fi
  legacy_link=${PRUKKA_LEGACY_LINK:-/usr/local/bin/prukka}
  legacy_override=${PRUKKA_LEGACY_LINK+x}

  case "$os" in
    darwin)
      state=${PRUKKA_STATE:-"$HOME/Library/Application Support/Prukka"}
      config=${PRUKKA_CONFIG:-"$state/config.yaml"}
      log_dir="$HOME/Library/Logs/Prukka"
      runtime_dir=""
      ;;
    linux)
      state=${PRUKKA_STATE:-"${XDG_STATE_HOME:-$HOME/.local/state}/prukka"}
      config=${PRUKKA_CONFIG:-"${XDG_CONFIG_HOME:-$HOME/.config}/prukka/config.yaml"}
      log_dir=""
      runtime_dir="${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/prukka}"
      ;;
    *) echo "unsupported OS: $os (Windows: use uninstall.ps1)" >&2; exit 1 ;;
  esac

  if [ "$purge" -eq 1 ]; then
    owned_dir "$state" && safe_user_path "$state" dir || {
      echo "refusing to purge unsafe state path: $state" >&2
      exit 1
    }
    [ "${config##*/}" = config.yaml ] && safe_user_path "$config" file || {
      echo "refusing to purge unsafe config path: $config" >&2
      exit 1
    }
    [ -z "$log_dir" ] || { owned_dir "$log_dir" && safe_user_path "$log_dir" dir; } || {
      echo "refusing to purge unsafe log path: $log_dir" >&2
      exit 1
    }
    [ -z "$runtime_dir" ] || safe_runtime_dir "$runtime_dir" || {
      echo "refusing to purge unsafe runtime path: $runtime_dir" >&2
      exit 1
    }
  fi

  cleanup_failed=0
  echo "==> removing the per-user service"
  if [ "$have_prukka" -eq 1 ] && "$prukka" service remove; then
    :
  else
    have_prukka=0
    if ! fallback_service_remove; then cleanup_failed=1; fi
  fi

  if [ "$have_prukka" -eq 0 ] && command -v pgrep >/dev/null 2>&1; then
    daemon_pids=$(pgrep -x prukka 2>/dev/null || true)
    if [ -n "$daemon_pids" ]; then
      echo "residue: foreground Prukka process(es): $daemon_pids" >&2
      cleanup_failed=1
    fi
  fi

  if [ "$have_prukka" -eq 1 ] && "$prukka" stats >/dev/null 2>&1; then
    echo "a foreground Prukka daemon is still running; stop it and retry" >&2
    exit 1
  fi

  echo "==> removing virtual devices (may prompt for sudo)"
  # Never elevate the per-user executable. The fixed platform cleanup uses
  # only system tools and exact Prukka-owned identities.
  if ! fallback_devices_remove; then cleanup_failed=1; fi

  if [ "$purge" -eq 1 ]; then
    echo "==> removing legacy provider credentials"
    if ! fallback_legacy_credentials_remove; then cleanup_failed=1; fi
  fi

  if [ -L "$legacy_link" ] &&
     [ "$(readlink "$legacy_link")" = "$expected_prukka" ]; then
    if ! no_symlink_ancestors "$legacy_link"; then
      echo "residue: refusing legacy link through symlinked ancestor $legacy_link" >&2
      cleanup_failed=1
    elif [ "$legacy_override" = x ]; then
      if ! rm -f "$legacy_link"; then
        echo "residue: $legacy_link" >&2
        cleanup_failed=1
      fi
    elif ! privileged rm -f "$legacy_link"; then
      echo "residue: $legacy_link" >&2
      cleanup_failed=1
    fi
  fi

  if [ -n "$expected_prukka" ]; then
    for path in "$expected_prukka" "$expected_prukka.old" "$expected_prukka.new"; do
      if ! remove_user_file "$path"; then
        echo "residue: refusing unsafe executable path $path" >&2
        cleanup_failed=1
      fi
    done
  fi
  bin_real=$(CDPATH= cd -P -- "$bin_dir" 2>/dev/null && pwd -P || true)
  if [ -n "$bin_real" ]; then
    (
      CDPATH= cd -P -- "$bin_dir" || exit 1
      [ "$(pwd -P)" = "$bin_real" ] || exit 1
      rm -f .prukka.old.* .prukka.new.* \
        .prukka-uninstall.old.* .prukka-uninstall.new.*
    ) || cleanup_failed=1
  fi

  if [ "$os" = darwin ]; then
    safe_user_path "$state" dir && remove_leaf_from_dir "$state" prukkad.sock || cleanup_failed=1
  elif [ -n "$runtime_dir" ]; then
    if safe_runtime_dir "$runtime_dir"; then
      remove_leaf_from_dir "$runtime_dir" prukkad.sock || cleanup_failed=1
      rmdir "$runtime_dir" 2>/dev/null || true
    else
      echo "residue: refusing unsafe runtime path $runtime_dir" >&2
      cleanup_failed=1
    fi
  else
    safe_user_path "$state" dir && remove_leaf_from_dir "$state" prukkad.sock || cleanup_failed=1
  fi

  if [ "$purge" -eq 1 ]; then
    echo "==> purging configuration, state and logs"
    if ! remove_user_file "$config"; then
      echo "residue: refusing changed config path $config" >&2
      cleanup_failed=1
    fi
    if ! remove_user_tree "$state"; then
      echo "residue: refusing changed state path $state" >&2
      cleanup_failed=1
    fi
    if [ -n "$log_dir" ] && ! remove_user_tree "$log_dir"; then
      echo "residue: refusing changed log path $log_dir" >&2
      cleanup_failed=1
    fi
    config_dir=${config%/*}
    remove_user_dir_if_empty "$config_dir" || cleanup_failed=1
  fi

  if [ "$0" = "$bin_dir/prukka-uninstall" ]; then
    remove_user_file "$0" || cleanup_failed=1
  fi
  remove_user_dir_if_empty "$bin_dir" || cleanup_failed=1

  if [ "$cleanup_failed" -ne 0 ]; then
    echo "Prukka user files were removed, but the exact system residues listed above need manual cleanup." >&2
    exit 1
  fi

  echo "Prukka uninstalled."
  if [ "$purge" -eq 0 ]; then
    echo "Configuration, state, logs and legacy provider credentials were retained; use --purge to remove them."
  fi
}

main "$@"

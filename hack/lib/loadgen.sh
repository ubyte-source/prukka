#!/usr/bin/env bash
# Shared load-gate helpers. Voice verification writes only its explicit
# per-run cache marker after decoding a completed HLS segment.

LOAD_MAX_SESSIONS=64
LOAD_MAX_SECONDS=86400

load_validate_positive_integer() {
  local name=$1 value=$2
  case "$value" in
    ''|*[!0-9]*|0*)
      echo "FAIL: $name must be a positive integer (got $value)" >&2
      return 2
      ;;
  esac
}

load_validate_minimum_integer() {
  local name=$1 value=$2 minimum=$3 LC_ALL=C

  load_validate_positive_integer "$name" "$value" || return
  if (( ${#value} < ${#minimum} )) ||
    { (( ${#value} == ${#minimum} )) && [[ "$value" < "$minimum" ]]; }; then
    echo "FAIL: $name must be at least $minimum (got $value)" >&2
    return 2
  fi
}

load_validate_maximum_integer() {
  local name=$1 value=$2 maximum=$3 LC_ALL=C

  load_validate_positive_integer "$name" "$value" || return
  if (( ${#value} > ${#maximum} )) ||
    { (( ${#value} == ${#maximum} )) && [[ "$value" > "$maximum" ]]; }; then
    echo "FAIL: $name must not exceed $maximum (got $value)" >&2
    return 2
  fi
}

load_validate_percentage() {
  local name=$1 value=$2 LC_ALL=C

  load_validate_positive_integer "$name" "$value" || return
  if (( ${#value} > 3 )) ||
    { (( ${#value} == 3 )) && [[ "$value" > 100 ]]; }; then
    echo "FAIL: $name must not exceed 100 (got $value)" >&2
    return 2
  fi
}

load_validate_sessions() {
  local name=$1 value=$2
  load_validate_positive_integer "$name" "$value" || return
  if (( ${#value} > ${#LOAD_MAX_SESSIONS} )) ||
    { (( ${#value} == ${#LOAD_MAX_SESSIONS} )) && [[ "$value" > "$LOAD_MAX_SESSIONS" ]]; }; then
    echo "FAIL: $name must not exceed the daemon limit of $LOAD_MAX_SESSIONS" >&2
    return 2
  fi
}

load_write_config() {
  local path=$1 sessions=$2
  (
    umask 077
    printf 'providers:\n  dispatch:\n    max_lanes: %s\n    max_sessions: %s\ndefaults:\n  bed: off\n  delay: 0s\n' \
      "$sessions" "$sessions" >"$path"
  )
}

load_session_state_count() {
  local state=$1
  LC_ALL=C awk -v metric="prukka_sessions_by_state{state=\"$state\"}" '
    $1 == metric { value = $2; found = 1 }
    END { if (found) print int(value); else print 0 }
  '
}

load_has_cue() {
  local base_url=$1 session=$2 language=$3

  curl -fsS --connect-timeout 1 --max-time 1 \
    "$base_url/$session/$language/subs.vtt" 2>/dev/null |
    grep -- "-->" >/dev/null
}

load_has_audio_segment() {
  local base_url=$1 session=$2 language=$3

  curl -fsS --connect-timeout 1 --max-time 1 \
    "$base_url/$session/audio/$language/index.m3u8" 2>/dev/null |
    grep '\.ts$' >/dev/null
}

load_has_voiced_take() {
  local log_file=$1 session=$2 language=$3

  LC_ALL=C grep -F '"msg":"voiced"' "$log_file" |
    grep -F "\"session\":\"$session\"" |
    grep -F "\"target\":\"$language\"" >/dev/null
}

# load_has_voiced_audio proves that a completed HLS segment contains a
# non-trivial decoded waveform. Load configs mute the source bed, so signal in
# this rendition can only come from the synthesized voice track.
load_has_voiced_audio() {
  local base_url=$1 session=$2 language=$3 ffmpeg=$4 cache_dir=$5
  local marker="$cache_dir/$session-$language.voiced" playlist segments

  [ ! -f "$marker" ] || return 0
  [ -x "$ffmpeg" ] || return 1

  playlist=$(curl -fsS --connect-timeout 1 --max-time 1 \
    "$base_url/$session/audio/$language/index.m3u8" 2>/dev/null) || return 1
  segments=$(printf '%s\n' "$playlist" | tr -d '\r' | \
    sed -n '/^seg[0-9][0-9][0-9][0-9][0-9]\.ts$/p')
  [ -n "$segments" ] || return 1

  if {
    while IFS= read -r segment; do
      curl -fsS --connect-timeout 1 --max-time 2 \
        "$base_url/$session/audio/$language/$segment"
    done <<<"$segments"
  } | "$ffmpeg" -nostdin -hide_banner -loglevel error -f mpegts -i pipe:0 \
    -map 0:a:0 -ac 1 -ar 16000 -f s16le pipe:1 2>/dev/null | \
    od -An -v -t d2 | awk '
      { for (i = 1; i <= NF; i++) if ($i < -256 || $i > 256) signal = 1 }
      END { exit signal ? 0 : 1 }
    '; then
    : >"$marker"
    return 0
  fi

  return 1
}

load_lane_outputs_ready() {
  local base_url=$1 log_file=$2 session=$3 source=$4 target=$5 ffmpeg=$6 cache_dir=$7

  load_has_cue "$base_url" "$session" "$source" &&
    load_has_cue "$base_url" "$session" "$target" &&
    load_has_voiced_take "$log_file" "$session" "$target" &&
    load_has_voiced_audio "$base_url" "$session" "$target" "$ffmpeg" "$cache_dir"
}

load_missing_lane_outputs() {
  local base_url=$1 log_file=$2 session=$3 source=$4 target=$5 ffmpeg=$6 cache_dir=$7 missing=""

  load_has_cue "$base_url" "$session" "$source" || missing="$missing $source-caption"
  load_has_cue "$base_url" "$session" "$target" || missing="$missing $target-caption"
  load_has_audio_segment "$base_url" "$session" "$target" || missing="$missing $target-audio-segment"
  load_has_voiced_take "$log_file" "$session" "$target" || missing="$missing $target-voiced-take"
  load_has_voiced_audio "$base_url" "$session" "$target" "$ffmpeg" "$cache_dir" || \
    missing="$missing $target-voiced-audio"
  echo "${missing# }"
}

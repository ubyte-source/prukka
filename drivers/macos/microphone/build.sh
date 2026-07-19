#!/usr/bin/env bash
# Prukka Microphone — identity over the shared HAL core in ../common.
exec "$(dirname "$0")/../common/build.sh" "$(dirname "$0")"

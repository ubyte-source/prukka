#!/usr/bin/env bash
# Prukka Speaker — identity over the shared HAL core in ../common.
exec "$(dirname "$0")/../common/build.sh" "$(dirname "$0")"

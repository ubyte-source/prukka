# shellcheck shell=bash
# gh-retry.sh — bounded retry with backoff for the transient GitHub failures
# (5xx, secondary rate limits, uploads.github.com 404s) that have aborted real
# publishes. Sourced, never executed:
#
#   source hack/ci/gh-retry.sh
#   gh_retry gh release upload "$TAG" --clobber "${assets[@]}"
#
# Wrap only idempotent commands: a non-idempotent mutation that succeeds while
# its response is lost would be retried into an error or a duplicate.
# Diagnostics name only the command word — an argument can carry a credential
# the runner's log masking does not catch once it is encoded.
#
# GH_RETRY_DELAY exists so tests can exercise the retry path in milliseconds.

gh_retry() {
  local attempt out delay="${GH_RETRY_DELAY:-20}"
  out=$(mktemp)
  for attempt in 1 2 3 4 5 6; do
    if "$@" > "$out"; then
      cat "$out"
      rm -f "$out"
      return 0
    fi
    if [ "$attempt" -eq 6 ]; then
      echo "gh-retry: $1 still failing after $attempt attempts" >&2
      rm -f "$out"
      return 1
    fi
    echo "gh-retry: $1 attempt $attempt failed; backing off before retry" >&2
    sleep $((attempt * delay))
  done
}

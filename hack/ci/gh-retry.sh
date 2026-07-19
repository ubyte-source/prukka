# shellcheck shell=bash
# gh-retry.sh — the ONE definition of bounded retry with backoff for the
# transient GitHub failures (5xx, secondary rate limits, uploads.github.com
# 404s) that have aborted real publishes. release.yml and build-engine.yml
# both source this file, so the protection cannot drift between the twins or
# land on one of them only.
#
# Usage (sourced, never executed):
#   source hack/ci/gh-retry.sh
#   gh_retry gh release upload "$TAG" --clobber "${assets[@]}"
#   release_id=$(gh_retry gh api "repos/$OWNER_REPO/releases/tags/$TAG" --jq .id)
#
# Contract:
#   - Runs the command up to 6 times; sleeps attempt*GH_RETRY_DELAY seconds
#     between attempts (the production default of 20 gives 20/40/60/80/100).
#     Returns the command's success immediately, or 1 after the sixth failure.
#   - Emits only the successful attempt's stdout: a failed attempt's partial
#     output is discarded, so command substitutions, pipelines and file
#     redirections never observe a half-written page or download.
#   - Streams stderr through unbuffered, so each attempt's own error stays
#     visible live in the log next to the retry notices.
#   - Diagnostics name only the command word, never its arguments: an argument
#     can carry a credential (e.g. a git http.extraheader value) that the
#     runner's log masking does not catch once it is encoded.
#   - Wrap only idempotent commands: reads, downloads to a truncating
#     redirect, and uploads under --clobber. A non-idempotent mutation (a
#     DELETE, a create) that succeeds while its response is lost would be
#     retried into an error or a duplicate — such call sites must reason in a
#     comment instead of wrapping.
#
# GH_RETRY_DELAY exists so tests can exercise the full retry path in
# milliseconds; production call sites never set it.

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

#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

profile=${1-coverage.out}
threshold=${2-80}
[ -n "$profile" ] && [ ! -d "$profile" ] || { echo 'coverage: expected an output file' >&2; exit 1; }
awk -v value="$threshold" 'BEGIN {
  if (value !~ /^[0-9]+([.][0-9]+)?$/ || value + 0 > 100) exit 1
}' || { echo 'coverage: threshold must be a number in 0..100' >&2; exit 1; }

# No pipelines around subprocesses: POSIX sh has no portable pipefail. A failed
# list/test/cover must never be replaced by the exit status of grep or awk.
package_list=$(go list ./...)
packages=$(printf '%s\n' "$package_list" | awk 'NF && $0 !~ /\/examples\//')
[ -n "$packages" ] || { echo 'coverage: no core packages' >&2; exit 1; }
temporary=$(mktemp "${TMPDIR:-/tmp}/chitu-coverage.XXXXXXXX")
cleanup() { rm -f "$temporary"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Package names are Go import paths, not arbitrary shell input.
# shellcheck disable=SC2086
go test -covermode=atomic -coverprofile="$temporary" $packages
[ -s "$temporary" ] || { echo 'coverage: empty profile' >&2; exit 1; }
awk 'NR == 1 { if ($0 !~ /^mode: (set|count|atomic)$/) exit 1; next }
     NF == 3 && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { statements += $2 }
     END { if (statements <= 0) exit 1 }' "$temporary" || {
  echo 'coverage: profile has no statements or invalid mode' >&2; exit 1;
}
report=$(go tool cover -func="$temporary")
coverage=$(printf '%s\n' "$report" | awk '
  /^total:/ { totals++; value = $3 }
  END {
    if (totals != 1 || value !~ /^[0-9]+([.][0-9]+)?%$/) exit 1
    sub(/%$/, "", value); if (value + 0 > 100) exit 1; print value
  }')
awk -v actual="$coverage" -v required="$threshold" 'BEGIN {
  printf "core coverage: %.1f%% (required: %.1f%%)\n", actual, required
  if (actual + 0 < required + 0) exit 1
}'
node "$root/scripts/check-critical-coverage.mjs" "$temporary" "$threshold"
# Only a successful gate replaces the caller's existing profile.
mv -f "$temporary" "$profile"
trap - EXIT HUP INT TERM

#!/usr/bin/env sh
set -eu

profile=${1:-coverage.out}
threshold=${2:-80}

packages=$(go list ./... | grep -v '/examples/')
go test -covermode=atomic -coverprofile="$profile" $packages

coverage=$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
if [ -z "$coverage" ]; then
  echo "coverage: total was not found in $profile" >&2
  exit 1
fi

awk -v actual="$coverage" -v required="$threshold" 'BEGIN {
  printf "core coverage: %.1f%% (required: %.1f%%)\n", actual, required
  if (actual + 0 < required + 0) exit 1
}'

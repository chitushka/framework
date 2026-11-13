#!/usr/bin/env sh
set -eu
project="chitu-admin-it-$(date +%s)-$$"
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then docker compose -p "$project" -f compose.admin-e2e.yml logs --no-color; fi
  docker compose -p "$project" -f compose.admin-e2e.yml down --volumes --remove-orphans --timeout 10 || result=1
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p .cache/admin-e2e/test-results .cache/admin-e2e/playwright-report
docker compose -p "$project" -f compose.admin-e2e.yml build
docker compose -p "$project" -f compose.admin-e2e.yml up -d --wait postgres mysql
docker compose -p "$project" -f compose.admin-e2e.yml run --rm --no-deps integration
docker compose -p "$project" -f compose.admin-e2e.yml run --rm --no-deps browser

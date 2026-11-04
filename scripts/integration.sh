#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
project="chitu-it-$(date +%s)-$$"
cleanup() {
    result=$?
    trap - EXIT
    if [ "$result" -ne 0 ]; then
        docker compose -p "$project" -f compose.integration.yml logs --no-color || true
    fi
    docker compose -p "$project" -f compose.integration.yml down --volumes --remove-orphans --timeout 10 || result=1
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker compose -p "$project" -f compose.integration.yml up --build --abort-on-container-exit --exit-code-from tests

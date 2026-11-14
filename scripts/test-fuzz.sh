#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/chitu-fuzz-test.XXXXXXXX")
cleanup() {
  case "$fixture" in "${TMPDIR:-/tmp}"/chitu-fuzz-test.*) rm -rf "$fixture" ;; *) exit 1 ;; esac
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$fixture/bin"
cat > "$fixture/bin/go" <<'GO'
#!/bin/sh
printf 'call\n' >> "$FUZZ_CALLS"
while [ "$#" -gt 0 ]; do
  case "$1" in
    -list)
      [ "$CASE" != list-failure ] || exit 41
      [ "$CASE" != missing ] || exit 0
      printf '%s\n' "$2" | tr -d '^$'
      exit 0 ;;
    -fuzz)
      [ "$CASE" != fuzz-failure ] || exit 42
      echo 'PASS'; exit 0 ;;
  esac
  shift
done
exit 43
GO
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH" FUZZ_LOG_DIR="$fixture/logs" FUZZ_CALLS="$fixture/calls"
for CASE in missing list-failure fuzz-failure; do
  export CASE
  : > "$FUZZ_CALLS"
  if sh "$root/scripts/fuzz.sh" 1x > "$fixture/output" 2>&1; then
    echo "fuzz: accepted negative case $CASE" >&2; exit 1
  fi
  expected=1
  if [ "$CASE" = fuzz-failure ]; then expected=2; fi
  [ "$(wc -l < "$FUZZ_CALLS" | tr -d ' ')" = "$expected" ] || { echo 'fuzz: runner continued after failure' >&2; exit 1; }
done
CASE=success; export CASE
: > "$FUZZ_CALLS"
sh "$root/scripts/fuzz.sh" 1x > "$fixture/output" 2>&1
[ "$(wc -l < "$FUZZ_CALLS" | tr -d ' ')" = 16 ] || { echo 'fuzz: not all eight targets ran' >&2; exit 1; }
echo 'fuzz: missing target, discovery/process failure and fail-fast tests passed'

#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/chitu-coverage-test.XXXXXXXX")
cleanup() { rm -rf "$fixture"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$fixture/bin"
cat > "$fixture/bin/go" <<'GO'
#!/bin/sh
case "$1" in
  list)
    [ "$CASE" != list-failure ] || exit 41
    if [ "$CASE" != no-packages ]; then printf 'fixture/core\nfixture/examples/demo\n'; fi ;;
  test)
    [ "$CASE" != test-failure ] || exit 42
    for arg in "$@"; do case "$arg" in -coverprofile=*) file=${arg#*=};; esac; done
    case "$CASE" in
      empty) : > "$file" ;;
      header-only) printf 'mode: atomic\n' > "$file" ;;
      zero-statements) printf 'mode: atomic\nfixture.go:1.1,1.2 0 0\n' > "$file" ;;
      critical-below)
        printf 'mode: atomic\n' > "$file"
        for pkg in auth session security; do
          printf 'github.com/chitushka/framework/%s/file.go:1.1,1.2 10 0\n' "$pkg" >> "$file"
        done ;;
      *)
        printf 'mode: atomic\n' > "$file"
        for pkg in auth session security; do
          printf 'github.com/chitushka/framework/%s/file.go:1.1,1.2 10 1\n' "$pkg" >> "$file"
        done
        if [ "$CASE" = core-rounded ]; then
          printf 'github.com/chitushka/framework/core/file.go:1.1,1.2 7970 1\ngithub.com/chitushka/framework/core/file.go:2.1,2.2 2001 0\n' >> "$file"
        fi ;;
    esac ;;
  tool)
    [ "$CASE" != cover-failure ] || exit 43
    case "$CASE" in
      missing-total) printf 'fixture.go:1: f 100.0%%\n' ;;
      duplicate-total) printf 'total: (statements) 90.0%%\ntotal: (statements) 99.0%%\n' ;;
      invalid-total) printf 'total: (statements) NaN%%\n' ;;
      excessive-total) printf 'total: (statements) 101.0%%\n' ;;
      below) printf 'total: (statements) 79.9%%\n' ;;
      core-rounded) printf 'total: (statements) 80.0%%\n' ;;
      *) printf 'total: (statements) 90.0%%\n' ;;
    esac ;;
  *) exit 44 ;;
esac
GO
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
for CASE in list-failure test-failure cover-failure no-packages empty header-only zero-statements missing-total duplicate-total invalid-total excessive-total below critical-below core-rounded; do
  export CASE
  printf 'previous profile\n' > "$fixture/result"
  if sh "$root/scripts/check-coverage.sh" "$fixture/result" 80 > "$fixture/log" 2>&1; then
    echo "coverage negative case accepted: $CASE" >&2; exit 1
  fi
  [ "$(cat "$fixture/result")" = 'previous profile' ] || { echo 'failed run destroyed previous profile' >&2; exit 1; }
done
CASE=success; export CASE
for threshold in '' -1 101 NaN Inf 80junk .5 1.2.3; do
  if sh "$root/scripts/check-coverage.sh" "$fixture/result" "$threshold" > "$fixture/log" 2>&1; then
    echo "invalid threshold accepted: $threshold" >&2; exit 1
  fi
done
sh "$root/scripts/check-coverage.sh" "$fixture/result" 90
grep -q '^mode: atomic$' "$fixture/result"
echo 'coverage: subprocess, profile, threshold and preservation tests passed'

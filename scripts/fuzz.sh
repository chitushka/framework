#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
duration=${1:-3s}
logs=${FUZZ_LOG_DIR:-.cache/fuzz}
mkdir -p "$logs"
# Keep workers bounded on large hosted machines. Each invocation selects exactly
# one target. Verify discovery explicitly: Go can succeed with no matching fuzz
# tests, which must not turn a renamed/deleted target into a passing gate.
export GOMAXPROCS=${GOMAXPROCS:-2}
for entry in web:FuzzRouterURLRoundTrip web:FuzzBindJSON db:FuzzQueryParameters db:FuzzRawPlaceholders cache:FuzzRESP auth:FuzzJWT auth:FuzzPasswordHash session:FuzzCookie; do
  package=${entry%:*}
  target=${entry#*:}
  log="$logs/$package-$target.log"
  echo "Fuzzing $package/$target for $duration"
  if go test "./$package" -list "^$target$" > "$log" 2>&1; then
    if ! grep -Fx "$target" "$log" > /dev/null; then
      cat "$log" >&2
      echo "fuzz: missing target $package/$target" >&2
      exit 1
    fi
  else
    result=$?
    cat "$log" >&2
    exit "$result"
  fi
  if go test "./$package" -run '^$' -fuzz "^$target$" -fuzztime "$duration" -parallel 2 >> "$log" 2>&1; then
    cat "$log"
  else
    result=$?
    cat "$log" >&2
    exit "$result"
  fi
done

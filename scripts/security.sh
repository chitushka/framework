#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mkdir -p .cache/security/bin
GOBIN="$root/.cache/security/bin" go -C tools/security install golang.org/x/vuln/cmd/govulncheck github.com/zricethezav/gitleaks/v8
for module in . adapters integration examples/advanced-admin tools/releasecheck; do
  name=$(printf '%s' "$module" | tr '/.' '__')
  # JSON/SARIF always exit 0, even on findings. Text mode preserves the scanner's
  # failure status; include tests so integration-only drivers are actually seen.
  (cd "$module" && "$root/.cache/security/bin/govulncheck" -format=text -test ./... > "$root/.cache/security/govulncheck-$name.txt")
done
.cache/security/bin/gitleaks dir --redact --ignore-gitleaks-allow --timeout 180 --config .gitleaks.toml --report-format json --report-path .cache/security/secrets-worktree.json .
.cache/security/bin/gitleaks git --redact --ignore-gitleaks-allow --timeout 180 --config .gitleaks.toml --log-opts=--all --report-format json --report-path .cache/security/secrets-history.json .

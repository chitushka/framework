#!/usr/bin/env sh
set -eu
docker build -f examples/basic/Dockerfile.e2e -t chitu-basic-e2e .
mkdir -p .cache/basic-e2e/test-results .cache/basic-e2e/playwright-report
docker run --rm \
  --mount "type=bind,source=$(pwd)/.cache/basic-e2e/test-results,target=/workspace/frontend/test-results" \
  --mount "type=bind,source=$(pwd)/.cache/basic-e2e/playwright-report,target=/workspace/frontend/playwright-report" \
  chitu-basic-e2e

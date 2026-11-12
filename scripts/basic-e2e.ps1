$ErrorActionPreference = 'Stop'
$docker = (Get-Command docker -ErrorAction Stop).Source
$results = Join-Path (Get-Location).Path '.cache/basic-e2e/test-results'
$report = Join-Path (Get-Location).Path '.cache/basic-e2e/playwright-report'
New-Item -ItemType Directory -Force -Path $results, $report | Out-Null
# Native stderr may contain progress/INFO even on success in Windows PowerShell.
$ErrorActionPreference = 'Continue'
& $docker build -f examples/basic/Dockerfile.e2e -t chitu-basic-e2e .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& $docker run --rm --mount "type=bind,source=$results,target=/workspace/frontend/test-results" --mount "type=bind,source=$report,target=/workspace/frontend/playwright-report" chitu-basic-e2e
exit $LASTEXITCODE

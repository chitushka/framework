$ErrorActionPreference = 'Stop'
$composeProject = 'chitu-it-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$composeFile = Join-Path $PSScriptRoot '../compose.integration.yml'
$testExitCode = 1
try {
    & docker compose -p $composeProject -f $composeFile up --build --abort-on-container-exit --exit-code-from tests
    $testExitCode = $LASTEXITCODE
} finally {
    if ($testExitCode -ne 0) {
        & docker compose -p $composeProject -f $composeFile logs --no-color
    }
    & docker compose -p $composeProject -f $composeFile down --volumes --remove-orphans --timeout 10
    if ($LASTEXITCODE -ne 0) { $testExitCode = 1 }
}
exit $testExitCode

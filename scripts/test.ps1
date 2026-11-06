<#
.SYNOPSIS
  Runs the test suites: backend (go vet + go test) and frontend (svelte-check + unit tests).
.PARAMETER Integration
  Also run the integration tests against the REAL local PostgreSQL and Kafka (they must be running
  and migrations applied). Without it those tests are skipped (and reported as skipped).
.PARAMETER BackendOnly / FrontendOnly
  Limit to one side.
#>
[CmdletBinding()]
param(
    [switch]$Integration,
    [switch]$BackendOnly,
    [switch]$FrontendOnly
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script

if ($Integration) {
    if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 9092)) {
        Write-Host "Kafka is not running. Start it with scripts/start-kafka.ps1 before running integration tests." -ForegroundColor Red
        exit 1
    }
}

$failed = @()

if (-not $FrontendOnly) {
    Assert-Command 'go' 'Install Go 1.22+.'
    Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
    try {
        if ($Integration) {
            $env:ZNERO_INTEGRATION = '1'
            $testDb = Get-Setting 'TEST_DATABASE_URL' $null
            if ($testDb) {
                $env:DATABASE_URL = $testDb
            } else {
                $env:DATABASE_URL = Get-Setting 'DATABASE_URL'
            }
        } else {
            Remove-Item Env:\ZNERO_INTEGRATION -ErrorAction SilentlyContinue
        }
        Write-Host '== backend: go vet ==' -ForegroundColor Cyan
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { $failed += 'backend vet' }
        Write-Host "== backend: go test (integration=$([bool]$Integration)) ==" -ForegroundColor Cyan
        & go test -count=1 -p=1 ./...
        if ($LASTEXITCODE -ne 0) { $failed += 'backend tests' }
    } finally {
        Remove-Item Env:\ZNERO_INTEGRATION -ErrorAction SilentlyContinue
        Remove-Item Env:\DATABASE_URL -ErrorAction SilentlyContinue
        Pop-Location
    }
}

if (-not $BackendOnly) {
    Assert-Command 'npm' 'Install Node.js LTS.'
    Push-Location -LiteralPath (Join-Path $script:RepoRoot 'frontend')
    try {
        if (-not (Test-Path -LiteralPath 'node_modules')) { & npm ci; if ($LASTEXITCODE -ne 0) { throw 'npm ci failed.' } }
        Write-Host '== frontend: svelte-check ==' -ForegroundColor Cyan
        & npm run check
        if ($LASTEXITCODE -ne 0) { $failed += 'frontend check' }
        Write-Host '== frontend: unit tests ==' -ForegroundColor Cyan
        & npm run test
        if ($LASTEXITCODE -ne 0) { $failed += 'frontend tests' }
    } finally {
        Pop-Location
    }
}

if ($failed.Count -gt 0) {
    Write-Host "FAILED: $($failed -join ', ')" -ForegroundColor Red
    exit 1
}
Write-Host 'All selected test suites passed.' -ForegroundColor Green

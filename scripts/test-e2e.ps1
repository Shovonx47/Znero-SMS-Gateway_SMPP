<#
.SYNOPSIS
  Executes the full Playwright E2E suite against an isolated test database (znero_test).
  Guarantees that znero_dev is never touched.
#>
[CmdletBinding()]
param(
    [string]$Reporter = "list"
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

Write-Host "== Playwright Isolated E2E Test Suite (znero_test) ==" -ForegroundColor Cyan

# 1. Verify PostgreSQL and Kafka are running
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 5432)) {
    Write-Host "FATAL: PostgreSQL is not running on 127.0.0.1:5432." -ForegroundColor Red
    exit 1
}
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 9092)) {
    Write-Host "FATAL: Kafka is not running on 127.0.0.1:9092. Start it with scripts/start-kafka.ps1." -ForegroundColor Red
    exit 1
}

$testDbUrl = Get-Setting 'TEST_DATABASE_URL' $null
if (-not $testDbUrl) {
    $testDbUrl = "postgres://znero_app:s3cretPASS@localhost:5432/znero_test?sslmode=disable"
}

Write-Host "[INFO] E2E Target Database: znero_test (isolated from znero_dev)" -ForegroundColor Green

# 2. Apply migrations on znero_test
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    $env:DATABASE_URL = $testDbUrl
    $env:APP_ENV = 'development'
    Write-Host "Applying database migrations on znero_test..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'migrate-up.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to apply migrations on znero_test"
    }

    # 3. Seed dev accounts and sample datasets into znero_test
    Write-Host "Seeding test accounts and fixtures into znero_test..." -ForegroundColor Gray
    & go run ./cmd/seeddev
    & go run ./cmd/seedmastersample
    & go run ./cmd/seedproviderssample
    & go run ./cmd/seedratessample
    & go run ./cmd/seedroutingsample
} finally {
    Pop-Location
}

# 4. Stop any existing backend on port 8080 to ensure test backend runs exclusively
$existingPort8080 = Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue
if ($existingPort8080) {
    Write-Host "Stopping existing process on port 8080..." -ForegroundColor Yellow
    Stop-Process -Id $existingPort8080.OwningProcess -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
}

# 5. Start Backend API server targeting znero_test
Write-Host "Starting isolated backend API server targeting znero_test on port 8080..." -ForegroundColor Cyan
$backendProc = Start-Process -FilePath "go" -ArgumentList "run ./cmd/api" -WorkingDirectory (Join-Path $script:RepoRoot 'backend') -Environment @{
    DATABASE_URL = $testDbUrl
    APP_ENV = "development"
    PORT = "8080"
} -PassThru

# Wait for backend health
$healthy = $false
for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Milliseconds 500
    try {
        $res = Invoke-RestMethod -Uri "http://127.0.0.1:8080/api/v1/health" -Method Get -TimeoutSec 2 -ErrorAction Stop
        if ($res.status -eq 'healthy' -or $res.status -eq 'degraded') {
            $healthy = $true
            break
        }
    } catch {}
}

if (-not $healthy) {
    Write-Host "FATAL: Backend API failed to start against znero_test within 15 seconds." -ForegroundColor Red
    if ($backendProc -and -not $backendProc.HasExited) {
        Stop-Process -Id $backendProc.Id -Force -ErrorAction SilentlyContinue
    }
    exit 1
}
Write-Host "[OK] Backend API is healthy and connected to znero_test" -ForegroundColor Green

# 6. Ensure Frontend is running on port 5173
$frontendRunning = Test-TcpPort -HostName '127.0.0.1' -Port 5173
$frontendProc = $null
if (-not $frontendRunning) {
    Write-Host "Starting frontend dev server on port 5173..." -ForegroundColor Cyan
    $frontendProc = Start-Process -FilePath "npm" -ArgumentList "run dev" -WorkingDirectory (Join-Path $script:RepoRoot 'frontend') -PassThru
    for ($i = 0; $i -lt 20; $i++) {
        Start-Sleep -Milliseconds 500
        if (Test-TcpPort -HostName '127.0.0.1' -Port 5173) {
            $frontendRunning = $true
            break
        }
    }
}

$e2eExitCode = 0
try {
    Write-Host "Running Playwright E2E test suite..." -ForegroundColor Cyan
    Push-Location -LiteralPath (Join-Path $script:RepoRoot 'frontend')
    & npx playwright test --reporter=$Reporter
    $e2eExitCode = $LASTEXITCODE
} finally {
    Pop-Location
    # Stop backend
    if ($backendProc -and -not $backendProc.HasExited) {
        Write-Host "Stopping isolated test backend process (PID $($backendProc.Id))..." -ForegroundColor Gray
        Stop-Process -Id $backendProc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($frontendProc -and -not $frontendProc.HasExited) {
        Write-Host "Stopping frontend dev process (PID $($frontendProc.Id))..." -ForegroundColor Gray
        Stop-Process -Id $frontendProc.Id -Force -ErrorAction SilentlyContinue
    }
}

if ($e2eExitCode -eq 0) {
    Write-Host "[SUCCESS] All E2E tests passed against isolated znero_test database." -ForegroundColor Green
} else {
    Write-Host "[FAILED] E2E test suite failed with exit code $e2eExitCode." -ForegroundColor Red
}
exit $e2eExitCode


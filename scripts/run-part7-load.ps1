<#
.SYNOPSIS
  Part 7 Mixed HTTP+SMPP 100k Load Benchmark, pprof Profiling, and Bounded Continuous Soak against znero_test.
#>
[CmdletBinding()]
param(
    [int]$HttpSingle = 20000,
    [int]$HttpBulkBatches = 600,
    [int]$BulkSize = 100,
    [int]$SmppCount = 20000,
    [string]$SoakDuration = '10m'
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script
$env:Path = "C:\msys64\ucrt64\bin;" + $env:Path
$env:CGO_ENABLED = "1"

$testDbUrl = Get-Setting 'TEST_DATABASE_URL'
if ($testDbUrl -notmatch '/znero_test([/?]|$)') {
    throw "SAFETY GUARD: TEST_DATABASE_URL must target znero_test"
}

$psql = 'C:\Program Files\PostgreSQL\17\bin\psql.exe'
$binDir = Join-Path $script:RepoRoot 'bin'
$tmpDir = Join-Path $script:RepoRoot '.tmp'
$pprofDir = Join-Path $tmpDir 'pprof'
if (-not (Test-Path -LiteralPath $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir | Out-Null }
$stateFile = Join-Path $tmpDir 'p7_state.json'

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "[1/6] Building binaries to bin\..." -ForegroundColor Cyan
    & go build -o (Join-Path $binDir 'znero.exe') ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero.exe failed" }
    & go build -o (Join-Path $binDir 'znero-smppmock.exe') ./cmd/smppmock
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-smppmock.exe failed" }
    & go build -o (Join-Path $binDir 'znero-e2ehttpprov.exe') ./cmd/e2ehttpprov
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-e2ehttpprov.exe failed" }
    & go build -o (Join-Path $binDir 'znero-loadgen.exe') ./cmd/loadgen
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-loadgen.exe failed" }
} finally {
    Pop-Location
}

$mockProc = $null
$httpProvProc = $null

try {
    Write-Host "[2/6] Preparing clean znero_test state & starting mock carriers + live 6 Znero processes (-WithPprof)..." -ForegroundColor Cyan
    $prevPref = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $psql $testDbUrl -v ON_ERROR_STOP=1 -At -c @"
UPDATE users SET mfa_enabled = false, mfa_secret_encrypted = NULL, failed_login_attempts = 0, locked_until = NULL WHERE email = 'dev-superadmin@dev.znero.test';
UPDATE provider_connections SET status = 'disabled' WHERE status = 'active';
UPDATE route_plans SET status = 'deactivated' WHERE status = 'active';
DELETE FROM customer_webhook_deliveries;
DELETE FROM smpp_dlr_queue;
"@ | Out-Null
    $ErrorActionPreference = $prevPref

    $mockProc = Start-Process -FilePath (Join-Path $binDir 'znero-smppmock.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19100', '-ctrl-addr', '127.0.0.1:19101', '-system-id', 'E2E_EXT_SMSC', '-password', 'extSmscPass123!', '-dlr-mode', 'immediate') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'smppmock-p7.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'smppmock-p7.err.log') -PassThru

    $httpProvProc = Start-Process -FilePath (Join-Path $binDir 'znero-e2ehttpprov.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19102', '-token', 'extHttpTok123!', '-callback-secret', 'extHttpCbSec123!') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'e2ehttpprov-p7.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'e2ehttpprov-p7.err.log') -PassThru

    $dl = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $dl) {
        if ((Test-TcpPort -Port 19100) -and (Test-TcpPort -Port 19102)) { break }
        Start-Sleep -Milliseconds 200
    }
    if (-not (Test-TcpPort -Port 19100) -or -not (Test-TcpPort -Port 19102)) { throw "Mock providers failed to start" }

    # Disable global per-IP rate limiter & tune DB pool within Postgres max_connections=100 (6 roles x 12 = 72 max)
    $env:RATE_LIMIT_MAX = '0'
    $env:DB_MAX_CONNS   = '12'
    & (Join-Path $PSScriptRoot 'dev-up.ps1') -Target test -NoFrontend -SkipBuild -WithPprof -KeepMocks
    if ($global:LASTEXITCODE -ne 0) { throw "dev-up.ps1 failed" }

    $lgExe = Join-Path $binDir 'znero-loadgen.exe'
    Remove-Item -LiteralPath $stateFile -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $pprofDir -Recurse -Force -ErrorAction SilentlyContinue

    Write-Host "[3/6] Provisioning 6 load-test customers (3 SMPP + 3 HTTP routes) via REST API..." -ForegroundColor Cyan
    & $lgExe -mode=setup -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen setup failed" }

    Write-Host "[4/6] Running 100k Mixed Load Burst (single=$HttpSingle, bulk=${HttpBulkBatches}x${BulkSize}, smpp=$SmppCount) + pprof capture..." -ForegroundColor Cyan
    & $lgExe -mode=burst -state-file $stateFile -db-url $testDbUrl -pprof-dir $pprofDir `
        -http-single $HttpSingle -http-bulk-batches $HttpBulkBatches -bulk-size $BulkSize -smpp-count $SmppCount
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen burst failed" }

    Write-Host "`n=== Top pprof CPU & Heap Functions Captured Under Load ===" -ForegroundColor Cyan
    foreach ($svc in @('api', 'smpp', 'dispatcher')) {
        $cpuFile = Join-Path $pprofDir "${svc}_cpu.pb.gz"
        $heapFile = Join-Path $pprofDir "${svc}_heap.pb.gz"
        if (Test-Path -LiteralPath $cpuFile) {
            Write-Host "--- $svc CPU top 10 ---" -ForegroundColor Yellow
            & go tool pprof -top -nodecount=10 $cpuFile 2>&1 | Select-Object -First 18
        }
        if (Test-Path -LiteralPath $heapFile) {
            Write-Host "--- $svc HEAP top 8 ---" -ForegroundColor Yellow
            & go tool pprof -top -nodecount=8 $heapFile 2>&1 | Select-Object -First 15
        }
    }

    Write-Host "[5/6] Running Bounded Continuous Mixed Soak ($SoakDuration) + Memory/Goroutine/DB Pool Sampling..." -ForegroundColor Cyan
    & $lgExe -mode=soak -state-file $stateFile -db-url $testDbUrl -duration $SoakDuration
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen soak failed" }

    Write-Host "[6/6] Running Post-Load SQL Ledger & HTTP Reconciliation Audit..." -ForegroundColor Cyan
    & $lgExe -mode=verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen verify failed" }

    Write-Host "`n=== PART 7 LOAD + PPROF + SOAK SUITE: ALL PASSED ===" -ForegroundColor Green
} finally {
    Write-Host "`n[CLEANUP] Stopping Znero + mocks..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'dev-down.ps1') -KeepKafka -Quiet | Out-Null
    if ($mockProc -and -not $mockProc.HasExited) { Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue }
    if ($httpProvProc -and -not $httpProvProc.HasExited) { Stop-Process -Id $httpProvProc.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name 'znero-e2ehttpprov','znero-smppmock' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

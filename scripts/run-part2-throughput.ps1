<#
.SYNOPSIS
  Part 2 Throughput Benchmark: Measures full E2E path (accept -> send -> DLR -> ledger capture -> webhook + SMPP deliver_sm) on znero_test.
  Does NOT touch znero_dev or the isolated Part 1 soak (bin\soak\).
#>
[CmdletBinding()]
param(
    [int]$HttpSingle = 3000,
    [int]$HttpBulkBatches = 40,
    [int]$BulkSize = 100,
    [int]$SmppCount = 5000,
    [int]$Dispatchers = 1,
    [int]$DbMaxConns = 8,
    [string]$PprofPhase = 'drain',
    [string]$DrainTimeout = '10m',
    [switch]$SynchronousCommitOff,
    [switch]$SkipBuild
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
if ($SynchronousCommitOff) {
    $env:PGOPTIONS = '-c synchronous_commit=off'
    Write-Host "[WARNING] EXPERIMENTAL CEILING TEST: synchronous_commit=off (never used for safety claims)" -ForegroundColor Yellow
} else {
    Remove-Item Env:\PGOPTIONS -ErrorAction SilentlyContinue
}

$psql = 'C:\Program Files\PostgreSQL\17\bin\psql.exe'
$binDir = Join-Path $script:RepoRoot 'bin'
$tmpDir = Join-Path $script:RepoRoot '.tmp'
$pprofDir = Join-Path $tmpDir 'pprof_part2'
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
New-Item -ItemType Directory -Force -Path $pprofDir | Out-Null
$stateFile = Join-Path $tmpDir 'p2_state.json'

if (-not $SkipBuild) {
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
}

$mockProc = $null
$httpProvProc = $null
$extraDispatchers = @()

try {
    Write-Host "[2/6] Cleaning znero_test messaging state & starting mocks + live services (dispatchers=$Dispatchers, db_max_conns=$DbMaxConns)..." -ForegroundColor Cyan
    $prevPref = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $psql $testDbUrl -v ON_ERROR_STOP=1 -At -c @"
TRUNCATE TABLE message_events, message_dlrs;
DELETE FROM settlement_exceptions;
DELETE FROM unmatched_dlrs;
DELETE FROM customer_webhook_attempts;
DELETE FROM customer_webhook_deliveries;
DELETE FROM smpp_dlr_queue;
DELETE FROM message_outbox;
DELETE FROM message_provider_lookups;
DELETE FROM message_idempotency_lookups;
DELETE FROM message_locator;
DELETE FROM message_attempts;
DELETE FROM message_parts;
DELETE FROM inbound_messages;
DELETE FROM messages;
DELETE FROM holds;
UPDATE users SET mfa_enabled = false, mfa_secret_encrypted = NULL, failed_login_attempts = 0, locked_until = NULL WHERE email = 'dev-superadmin@dev.znero.test';
UPDATE provider_connections SET status = 'disabled' WHERE status = 'active';
UPDATE route_plans SET status = 'deactivated' WHERE status = 'active';
"@ | Out-Null
    $ErrorActionPreference = $prevPref

    $mockProc = Start-Process -FilePath (Join-Path $binDir 'znero-smppmock.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19100', '-ctrl-addr', '127.0.0.1:19101', '-system-id', 'E2E_EXT_SMSC', '-password', 'extSmscPass123!', '-dlr-mode', 'immediate') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'smppmock-p2.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'smppmock-p2.err.log') -PassThru

    $httpProvProc = Start-Process -FilePath (Join-Path $binDir 'znero-e2ehttpprov.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19102', '-token', 'extHttpTok123!', '-callback-secret', 'extHttpCbSec123!') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'e2ehttpprov-p2.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'e2ehttpprov-p2.err.log') -PassThru

    $dl = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $dl) {
        if ((Test-TcpPort -Port 19100) -and (Test-TcpPort -Port 19102)) { break }
        Start-Sleep -Milliseconds 200
    }
    if (-not (Test-TcpPort -Port 19100) -or -not (Test-TcpPort -Port 19102)) { throw "Mock providers failed to start" }

    $env:DATABASE_URL = $testDbUrl
    $env:RATE_LIMIT_MAX = '0'
    $env:DB_MAX_CONNS = "$DbMaxConns"
    & (Join-Path $PSScriptRoot 'dev-up.ps1') -Target test -NoFrontend -SkipBuild -WithPprof -KeepMocks
    if ($global:LASTEXITCODE -ne 0) { throw "dev-up.ps1 failed" }

    # Start extra dispatcher processes (if Dispatchers > 1)
    for ($d = 2; $d -le $Dispatchers; $d++) {
        $dp = Start-Process -FilePath (Join-Path $binDir 'znero.exe') `
            -ArgumentList @('-role=dispatcher', "-pprof-addr=127.0.0.1:$((6068 + $d))") `
            -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $script:RunDir "znero-dispatcher-$d.out.log") `
            -RedirectStandardError (Join-Path $script:RunDir "znero-dispatcher-$d.err.log") -PassThru
        $extraDispatchers += $dp
        Write-Host "  Started extra znero-dispatcher #$d PID=$($dp.Id)" -ForegroundColor Gray
    }

    $lgExe = Join-Path $binDir 'znero-loadgen.exe'
    Remove-Item -LiteralPath $stateFile -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $pprofDir -Recurse -Force -ErrorAction SilentlyContinue

    Write-Host "[3/6] Provisioning 6 benchmark customers with webhooks..." -ForegroundColor Cyan
    & $lgExe -mode=setup -state-file $stateFile -db-url $testDbUrl -webhook-url 'http://127.0.0.1:19105/hook'
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen setup failed" }

    $totalN = $HttpSingle + ($HttpBulkBatches * $BulkSize) + $SmppCount
    Write-Host "[4/6] Running Full-Path Benchmark (total=${totalN}: single=$HttpSingle, bulk=${HttpBulkBatches}x${BulkSize}, smpp=$SmppCount, pprof=$PprofPhase)..." -ForegroundColor Cyan
    & $lgExe -mode=burst -state-file $stateFile -db-url $testDbUrl -pprof-dir $pprofDir -pprof-phase $PprofPhase `
        -webhook-listen '127.0.0.1:19105' -wait-webhooks -drain-timeout $DrainTimeout `
        -http-single $HttpSingle -http-bulk-batches $HttpBulkBatches -bulk-size $BulkSize -smpp-count $SmppCount
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen burst failed" }

    Write-Host "`n[5/6] Top pprof CPU Functions (phase=$PprofPhase)..." -ForegroundColor Cyan
    foreach ($svc in @('api', 'smpp', 'dispatcher', 'webhook')) {
        $cpuFile = Join-Path $pprofDir "${svc}_cpu.pb.gz"
        if (Test-Path -LiteralPath $cpuFile) {
            Write-Host "--- $svc CPU cum top 10 ---" -ForegroundColor Yellow
            & go tool pprof -top -cum -nodecount=10 $cpuFile 2>&1 | Select-Object -First 18
        }
    }

    Write-Host "`n[6/6] Running Post-Benchmark SQL Ledger & Reconciliation Audit..." -ForegroundColor Cyan
    & $lgExe -mode=verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "loadgen verify failed" }

    Write-Host "`n=== PART 2 THROUGHPUT BENCHMARK PASSED ===" -ForegroundColor Green
} finally {
    foreach ($dp in $extraDispatchers) {
        if ($dp -and -not $dp.HasExited) { Stop-Process -Id $dp.Id -Force -ErrorAction SilentlyContinue }
    }
    & (Join-Path $PSScriptRoot 'dev-down.ps1') -KeepKafka -Quiet | Out-Null
    if ($mockProc -and -not $mockProc.HasExited) { Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue }
    if ($httpProvProc -and -not $httpProvProc.HasExited) { Stop-Process -Id $httpProvProc.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name 'znero-e2ehttpprov','znero-smppmock' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

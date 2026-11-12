<#
.SYNOPSIS
  Part 6 Real OS-Process Resilience Suite against znero_test.
  Verifies:
    1. Fail-fast startup config safety (production+loopback, short JWT/MFA keys, non-loopback pprof)
    2. Brief DB outage + /healthz vs /readyz auto-recovery via isolated TCP proxy
    3. Live 6-process Kafka outage & restart mid-load + /readyz 503->200 recovery
    4. Flapping HTTP/SMPP carrier chaos (502, 429, HTML, binary, slow timeout, SMPP 0x58 + latency)
    5. Customer Edge 429 backpressure + 0 duplicate carrier sends + 0 SQL/reconciliation discrepancies
#>
[CmdletBinding()]
param()

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
if (-not (Test-Path -LiteralPath $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir | Out-Null }
$stateFile = Join-Path $tmpDir 'p6_state.json'
$expectedFile = Join-Path $tmpDir 'p6_expected.json'

# 1. Build latest binaries
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "[1/6] Building OS binaries to bin\..." -ForegroundColor Cyan
    & go build -o (Join-Path $binDir 'znero.exe') ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero.exe failed" }
    & go build -o (Join-Path $binDir 'znero-smppmock.exe') ./cmd/smppmock
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-smppmock.exe failed" }
    & go build -o (Join-Path $binDir 'znero-e2ehttpprov.exe') ./cmd/e2ehttpprov
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-e2ehttpprov.exe failed" }
    & go build -o (Join-Path $binDir 'znero-e2eblackbox.exe') ./cmd/e2eblackbox
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-e2eblackbox.exe failed" }
    & go build -o (Join-Path $binDir 'znero-part6resilience.exe') ./cmd/part6resilience
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-part6resilience.exe failed" }
} finally {
    Pop-Location
}

$zneroExe = Join-Path $binDir 'znero.exe'
$p6Exe = Join-Path $binDir 'znero-part6resilience.exe'
$bbExe = Join-Path $binDir 'znero-e2eblackbox.exe'

function Assert-FailFastConfig {
    param(
        [string]$Label,
        [hashtable]$EnvOverrides,
        [string[]]$ExtraArgs = @('-role=api'),
        [string]$ExpectMatch
    )
    $psi = [System.Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $zneroExe
    $psi.Arguments = ($ExtraArgs -join ' ')
    $psi.WorkingDirectory = $script:RepoRoot
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($k in $EnvOverrides.Keys) {
        $psi.Environment[$k] = $EnvOverrides[$k]
    }
    $proc = [System.Diagnostics.Process]::Start($psi)
    if (-not $proc.WaitForExit(5000)) {
        $proc.Kill()
        throw "FAIL-FAST [$Label]: znero.exe did not exit within 5s (should reject bad config immediately!)"
    }
    $out = $proc.StandardOutput.ReadToEnd() + "`n" + $proc.StandardError.ReadToEnd()
    if ($proc.ExitCode -eq 0) {
        throw "FAIL-FAST [$Label]: expected non-zero exit code, got 0. Output: $out"
    }
    if ($ExpectMatch -and ($out -notmatch [regex]::Escape($ExpectMatch))) {
        throw "FAIL-FAST [$Label]: exit=$($proc.ExitCode) output missing '$ExpectMatch': $out"
    }
    Write-Host ("  [PASS] Fail-Fast [$Label]: exit={0} rejected bad config" -f $proc.ExitCode) -ForegroundColor Green
}

$mockProc = $null
$httpProvProc = $null

try {
    Write-Host "[2/6] Verifying Fail-Fast Bad Startup Configs..." -ForegroundColor Cyan
    Assert-FailFastConfig -Label 'production+ALLOW_LOOPBACK_IN_TEST' -EnvOverrides @{
        APP_ENV = 'production'; ALLOW_LOOPBACK_IN_TEST = 'true'; DATABASE_URL = $testDbUrl
    } -ExpectMatch 'ALLOW_LOOPBACK_IN_TEST cannot be enabled in production'
    Assert-FailFastConfig -Label 'short JWT_ACCESS_SECRET' -EnvOverrides @{
        APP_ENV = 'test'; JWT_ACCESS_SECRET = 'too_short'; DATABASE_URL = $testDbUrl
    } -ExpectMatch 'JWT_ACCESS_SECRET must be at least 32'
    Assert-FailFastConfig -Label 'non-loopback pprof' -EnvOverrides @{
        APP_ENV = 'test'; DATABASE_URL = $testDbUrl
    } -ExtraArgs @('-role=api', '-pprof-addr=0.0.0.0:6060') -ExpectMatch 'must bind strictly to loopback'

    Write-Host "[3/6] Verifying DB Brief Outage + /healthz vs /readyz Auto-Recovery..." -ForegroundColor Cyan
    & (Join-Path $PSScriptRoot 'start-kafka.ps1') -TimeoutSeconds 60
    if ($global:LASTEXITCODE -ne 0) { throw "start-kafka.ps1 before db-proxy failed" }
    & $p6Exe -mode=db-proxy -znero-exe $zneroExe -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "db-proxy resilience test failed" }

    # Clean runtime slate & boot live 6-process OS stack + mocks
    Write-Host "[4/6] Starting mock carriers + live 6 Znero OS processes..." -ForegroundColor Cyan
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
        -RedirectStandardOutput (Join-Path $script:RunDir 'smppmock-p6.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'smppmock-p6.err.log') -PassThru

    $httpProvProc = Start-Process -FilePath (Join-Path $binDir 'znero-e2ehttpprov.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19102', '-token', 'extHttpTok123!', '-callback-secret', 'extHttpCbSec123!') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'e2ehttpprov-p6.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'e2ehttpprov-p6.err.log') -PassThru

    $dl = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $dl) {
        if ((Test-TcpPort -Port 19100) -and (Test-TcpPort -Port 19101) -and (Test-TcpPort -Port 19102)) { break }
        Start-Sleep -Milliseconds 200
    }
    if (-not (Test-TcpPort -Port 19101) -or -not (Test-TcpPort -Port 19102)) { throw "Mock providers failed to start" }

    & (Join-Path $PSScriptRoot 'dev-up.ps1') -Target test -NoFrontend -SkipBuild -KeepMocks
    if ($global:LASTEXITCODE -ne 0) { throw "dev-up.ps1 failed" }

    Remove-Item -LiteralPath $stateFile, $expectedFile -Force -ErrorAction SilentlyContinue
    & $bbExe -mode=setup -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "e2eblackbox setup failed" }

    # Enable auto_callback on e2ehttpprov for normal delivery flow
    & $p6Exe -mode=set-http-mode -http-mode=ok -auto-cb=true
    if ($global:LASTEXITCODE -ne 0) { throw "enable auto-cb failed" }

    Write-Host "[5/6] Kafka Outage & Restart Mid-Load (/healthz=200 vs /readyz=503->200)..." -ForegroundColor Cyan
    & $p6Exe -mode=probe-url -probe-url http://127.0.0.1:8080/readyz -expect-code 200
    if ($global:LASTEXITCODE -ne 0) { throw "pre-kafka-stop /readyz check failed" }
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=http -prefix=KAFPRE -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "send-batch KAFPRE http failed" }
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=smpp -prefix=KAFPRE -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "send-batch KAFPRE smpp failed" }

    & (Join-Path $PSScriptRoot 'stop-kafka.ps1') -TimeoutSeconds 30 -Force
    if ($global:LASTEXITCODE -ne 0) { throw "stop-kafka.ps1 failed" }

    & $p6Exe -mode=probe-url -probe-url http://127.0.0.1:8080/healthz -expect-code 200
    if ($global:LASTEXITCODE -ne 0) { throw "/healthz failed during Kafka outage" }
    & $p6Exe -mode=probe-url -probe-url http://127.0.0.1:8080/readyz -expect-code 503
    if ($global:LASTEXITCODE -ne 0) { throw "/readyz should return 503 during Kafka outage" }

    # Send while Kafka broker is stopped -> outbox & delivery continue cleanly without 5xx!
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=http -prefix=KAFDOWN -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "send-batch KAFDOWN failed" }
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=smpp -prefix=KAFDOWN -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "send-batch KAFDOWN smpp failed" }

    & (Join-Path $PSScriptRoot 'start-kafka.ps1') -TimeoutSeconds 60
    if ($global:LASTEXITCODE -ne 0) { throw "start-kafka.ps1 failed" }

    $recDl = (Get-Date).AddSeconds(20)
    $recOK = $false
    while ((Get-Date) -lt $recDl) {
        $prevPref = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & $p6Exe -mode=probe-url -probe-url http://127.0.0.1:8080/readyz -expect-code 200 *> $null
        $probeRc = $global:LASTEXITCODE
        $ErrorActionPreference = $prevPref
        if ($probeRc -eq 0) { $recOK = $true; break }
        Start-Sleep -Milliseconds 500
    }
    if (-not $recOK) { throw "/readyz did not recover to 200 after Kafka restart" }
    Write-Host "  [PASS] Kafka outage & restart mid-load survived cleanly (/readyz 503->200)" -ForegroundColor Green

    Write-Host "[6/6] Flapping/Garbage/Slow Provider Chaos + 429 Backpressure + Zero Duplicates/Discrepancies..." -ForegroundColor Cyan
    foreach ($chaosMode in @('502', '429', 'html', 'binary', 'slow')) {
        & $p6Exe -mode=set-http-mode -http-mode=$chaosMode -auto-cb=false
        & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=http -prefix="CHAOS_$chaosMode" -count=2 -http-mode=$chaosMode
    }
    & $p6Exe -mode=set-smpp-ctrl -smpp-err=0x58 -smpp-latency-ms=250
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=smpp -prefix=CHAOS_SMPP -count=2 -smpp-err=0x58

    # Restore healthy carriers and send final recovery batch
    & $p6Exe -mode=set-http-mode -http-mode=ok -auto-cb=true
    & $p6Exe -mode=set-smpp-ctrl -smpp-err=0x00 -smpp-latency-ms=0
    Start-Sleep -Seconds 3
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=http -prefix=RECOV -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "recovery http batch failed" }
    & $p6Exe -mode=send-batch -state-file $stateFile -expected-file $expectedFile -channel=smpp -prefix=RECOV -count=5
    if ($global:LASTEXITCODE -ne 0) { throw "recovery smpp batch failed" }

    # Customer Edge 429 rate-limit backpressure verification
    & $p6Exe -mode=burst-429 -state-file $stateFile
    if ($global:LASTEXITCODE -ne 0) { throw "burst-429 verification failed" }

    # Wait briefly for any transient slow/retry messages to terminate cleanly, then verify 0 dupes & 0 discrepancies
    Start-Sleep -Seconds 6
    & $p6Exe -mode=verify-chaos -state-file $stateFile -expected-file $expectedFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "verify-chaos failed" }

    Write-Host "`n=== PART 6 LIVE OS-PROCESS RESILIENCE SUITE: ALL PASSED ===" -ForegroundColor Green
} finally {
    Write-Host "`n[CLEANUP] Stopping Znero + mocks..." -ForegroundColor Gray
    # Guarantee Kafka is left running for subsequent parts
    & (Join-Path $PSScriptRoot 'start-kafka.ps1') -TimeoutSeconds 30 2>$null | Out-Null
    & (Join-Path $PSScriptRoot 'dev-down.ps1') -KeepKafka -Quiet | Out-Null
    if ($mockProc -and -not $mockProc.HasExited) { Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue }
    if ($httpProvProc -and -not $httpProvProc.HasExited) { Stop-Process -Id $httpProvProc.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name 'znero-e2ehttpprov','znero-smppmock' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $stateFile, $expectedFile -Force -ErrorAction SilentlyContinue
}

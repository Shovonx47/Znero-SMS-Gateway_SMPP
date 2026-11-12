<#
.SYNOPSIS
  Part 2 Real OS-Process Black-Box E2E Suite against znero_test.
  Starts all Znero services + smppmock + e2ehttpprov as separate OS processes,
  runs REST setup + 2x2 matrix + SMPP wire edge cases + Python smpplib client test +
  4 kill-9 component crashes + graceful drain, then tears down cleanly.
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
    throw "SAFETY GUARD: TEST_DATABASE_URL must target znero_test, got $(Mask-ConnectionString $testDbUrl)"
}

$psql = 'C:\Program Files\PostgreSQL\17\bin\psql.exe'
if (-not (Test-Path -LiteralPath $psql)) {
    throw "psql.exe not found at $psql"
}

$binDir = Join-Path $script:RepoRoot 'bin'
$tmpDir = Join-Path $script:RepoRoot '.tmp'
if (-not (Test-Path -LiteralPath $tmpDir)) {
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
}
$stateFile = Join-Path $tmpDir 'bb_state.json'
$pyVenv = Join-Path $tmpDir 'py-smpp-venv'

# 1. Build latest binaries
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "[1/7] Building OS binaries to bin\..." -ForegroundColor Cyan
    & go build -o (Join-Path $binDir 'znero.exe') ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero.exe failed" }
    & go build -o (Join-Path $binDir 'znero-smppmock.exe') ./cmd/smppmock
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-smppmock.exe failed" }
    & go build -o (Join-Path $binDir 'znero-e2ehttpprov.exe') ./cmd/e2ehttpprov
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-e2ehttpprov.exe failed" }
    & go build -o (Join-Path $binDir 'znero-e2eblackbox.exe') ./cmd/e2eblackbox
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-e2eblackbox.exe failed" }
} finally {
    Pop-Location
}

function Invoke-TestSQL {
    param([Parameter(Mandatory)][string]$Sql)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $out = & $psql $testDbUrl -v ON_ERROR_STOP=1 -At -c $Sql 2>&1
        if ($global:LASTEXITCODE -ne 0) {
            throw "psql failed: $out"
        }
        return ($out | Out-String).Trim()
    } finally {
        $ErrorActionPreference = $prev
    }
}

$mockProc = $null
$httpProvProc = $null

function Restart-ZneroRole {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Role,
        [int]$WaitPort = 0
    )
    $pidFile = Join-Path $script:RunDir "$Name.pid"
    if (Test-Path -LiteralPath $pidFile) {
        $oldPid = (Get-Content -LiteralPath $pidFile -ErrorAction SilentlyContinue).Trim()
        if ($oldPid -match '^\d+$') {
            Stop-Process -Id ([int]$oldPid) -Force -ErrorAction SilentlyContinue
            Start-Sleep -Milliseconds 300
        }
    }
    # Start with exact znero_test runtime env
    $env:DATABASE_URL = $testDbUrl
    $env:DB_NAME = 'znero_test'
    $env:APP_ENV = 'test'
    $env:ALLOW_LOOPBACK_IN_TEST = 'true'

    $outLog = Join-Path $script:RunDir "$Name.out.log"
    $errLog = Join-Path $script:RunDir "$Name.err.log"
    $proc = Start-Process -FilePath (Join-Path $binDir 'znero.exe') -ArgumentList @("-role=$Role") `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru
    Set-Content -LiteralPath $pidFile -Value $proc.Id
    Write-Host ("  [KILL-9 & RESTART] {0} (-role={1}) new PID={2}" -f $Name, $Role, $proc.Id) -ForegroundColor Yellow

    if ($WaitPort -gt 0) {
        $dl = (Get-Date).AddSeconds(25)
        while ((Get-Date) -lt $dl) {
            if (Test-TcpPort -HostName '127.0.0.1' -Port $WaitPort) { break }
            Start-Sleep -Milliseconds 250
        }
        if (-not (Test-TcpPort -HostName '127.0.0.1' -Port $WaitPort)) {
            throw "$Name failed to reopen port $WaitPort after crash restart"
        }
    } else {
        Start-Sleep -Seconds 2
    }
}

try {
    # 2. Prepare clean znero_test runtime baseline
    Write-Host "[2/7] Preparing clean znero_test runtime state..." -ForegroundColor Cyan
    Invoke-TestSQL @"
UPDATE users SET mfa_enabled = false, mfa_secret_encrypted = NULL, failed_login_attempts = 0, locked_until = NULL WHERE email = 'dev-superadmin@dev.znero.test';
UPDATE users SET failed_login_attempts = 0, locked_until = NULL WHERE email LIKE '%@dev.znero.test';
UPDATE provider_connections SET status = 'disabled' WHERE status = 'active';
UPDATE route_plans SET status = 'deactivated' WHERE status = 'active';
DELETE FROM customer_webhook_deliveries;
DELETE FROM smpp_dlr_queue;
DELETE FROM unmatched_dlrs;
DELETE FROM settlement_exceptions;
"@ | Out-Null

    # 3. Start external mock SMSC + external fake HTTP provider
    Write-Host "[3/7] Starting external carrier simulators (smppmock :19100 + e2ehttpprov :19102)..." -ForegroundColor Cyan
    $mockOut = Join-Path $script:RunDir 'smppmock-bb.out.log'
    $mockErr = Join-Path $script:RunDir 'smppmock-bb.err.log'
    $mockProc = Start-Process -FilePath (Join-Path $binDir 'znero-smppmock.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19100', '-ctrl-addr', '127.0.0.1:19101', '-system-id', 'E2E_EXT_SMSC', '-password', 'extSmscPass123!', '-dlr-mode', 'immediate') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden -RedirectStandardOutput $mockOut -RedirectStandardError $mockErr -PassThru

    $httpOut = Join-Path $script:RunDir 'e2ehttpprov-bb.out.log'
    $httpErr = Join-Path $script:RunDir 'e2ehttpprov-bb.err.log'
    $httpProvProc = Start-Process -FilePath (Join-Path $binDir 'znero-e2ehttpprov.exe') `
        -ArgumentList @('-addr', '127.0.0.1:19102', '-token', 'extHttpTok123!', '-callback-secret', 'extHttpCbSec123!') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden -RedirectStandardOutput $httpOut -RedirectStandardError $httpErr -PassThru

    $waitDl = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $waitDl) {
        if ((Test-TcpPort -Port 19100) -and (Test-TcpPort -Port 19101) -and (Test-TcpPort -Port 19102)) { break }
        Start-Sleep -Milliseconds 200
    }
    if (-not (Test-TcpPort -Port 19101) -or -not (Test-TcpPort -Port 19102)) {
        throw "External carrier simulators failed to start"
    }

    # 4. Boot all Znero OS services via dev-up.ps1
    Write-Host "[4/7] Starting Znero OS processes via scripts\dev-up.ps1..." -ForegroundColor Cyan
    & (Join-Path $PSScriptRoot 'dev-up.ps1') -Target test -NoFrontend -SkipBuild -KeepMocks
    if ($global:LASTEXITCODE -ne 0) { throw "dev-up.ps1 failed" }

    # 5. Setup & Verify 2x2 Matrix + SMPP Protocol Edge Cases + Direct SQL Ledger + Reconciliation
    Write-Host "[5/7] Running external black-box setup & verification..." -ForegroundColor Cyan
    Remove-Item -LiteralPath $stateFile -Force -ErrorAction SilentlyContinue
    $bbExe = Join-Path $binDir 'znero-e2eblackbox.exe'
    & $bbExe -mode=setup -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "e2eblackbox -mode=setup failed" }

    & $bbExe -mode=verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "e2eblackbox -mode=verify failed" }

    # 6. Independent third-party Python smpplib client test
    Write-Host "[6/7] Running independent Python smpplib third-party client test..." -ForegroundColor Cyan
    $st = Get-Content -LiteralPath $stateFile -Raw | ConvertFrom-Json
    if (Test-Path -LiteralPath $pyVenv) { Remove-Item -LiteralPath $pyVenv -Recurse -Force }
    $sysPy = @(
        "$env:LocalAppData\Programs\Python\Python312\python.exe",
        "$env:LocalAppData\Python\pythoncore-3.14-64\python.exe",
        'C:\msys64\ucrt64\bin\python.exe'
    ) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
    if (-not $sysPy) { throw "No valid Python interpreter found for smpplib verification" }
    & $sysPy -m venv $pyVenv
    if ($global:LASTEXITCODE -ne 0) { throw "python -m venv failed ($sysPy)" }
    $pyExe = @((Join-Path $pyVenv 'Scripts\python.exe'), (Join-Path $pyVenv 'bin\python.exe')) |
        Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
    if (-not $pyExe) { throw "venv python executable not found under $pyVenv" }
    & $pyExe -m pip install --quiet smpplib
    if ($global:LASTEXITCODE -ne 0) { throw "pip install smpplib failed" }
    & $pyExe (Join-Path $PSScriptRoot 'test-smpplib-client.py') --host 127.0.0.1 --port 2776 --system-id $st.cust1_smpp_sys --password $st.cust1_smpp_pass
    if ($global:LASTEXITCODE -ne 0) { throw "Python smpplib client verification failed" }

    # 7. Four kill -9 crash scenarios + 1 graceful drain test
    Write-Host "[7/7] Running 4 kill-9 crash recovery tests + graceful drain..." -ForegroundColor Cyan

    # Crash 1: Dispatcher kill -9 mid-flight -> restart -> 0 loss, 0 duplicates
    & $bbExe -mode=crash-http-send -count=5 -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-http-send (1) failed" }
    Restart-ZneroRole -Name 'znero-dispatcher' -Role 'dispatcher'
    Start-Sleep -Seconds 6  # allow route/rate cache sync
    & $bbExe -mode=crash-verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-verify (1 dispatcher) failed" }

    # Crash 2: API killed immediately after HTTP 202 -> restart -> queued messages still deliver
    & $bbExe -mode=crash-http-send -count=3 -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-http-send (2) failed" }
    Restart-ZneroRole -Name 'znero-api' -Role 'api' -WaitPort 8080
    & $bbExe -mode=crash-verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-verify (2 api) failed" }

    # Crash 3: Webhook worker kill -9 with pending customer webhook delivery -> restart -> delivery completes
    $whListener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $whListener.Start()
    $whPort = $whListener.LocalEndpoint.Port
    $whUrl = "http://127.0.0.1:$whPort/crash-wh"
    $whRegBody = @{
        url         = $whUrl
        events      = @('message.delivered', 'message.failed')
        description = 'crash-3 webhook test'
    } | ConvertTo-Json
    $whReg = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8080/api/v1/customer/webhooks' `
        -Headers @{ 'Authorization' = "Bearer $($st.cust1_api_key)"; 'Content-Type' = 'application/json' } -Body $whRegBody
    if (-not $whReg) { throw "failed registering crash-3 webhook" }

    & $bbExe -mode=crash-http-send -count=2 -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-http-send (3) failed" }
    Restart-ZneroRole -Name 'znero-webhook' -Role 'webhook'
    # Accept incoming HTTP webhook POST(s)
    $whReceived = 0
    $whDl = (Get-Date).AddSeconds(25)
    while ((Get-Date) -lt $whDl -and $whReceived -lt 2) {
        if ($whListener.Pending()) {
            $cli = $whListener.AcceptTcpClient()
            try {
                $stream = $cli.GetStream()
                $buf = New-Object byte[] 8192
                $null = $stream.Read($buf, 0, $buf.Length)
                $respBytes = [System.Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK`r`nContent-Length: 2`r`nConnection: close`r`n`r`nok")
                $stream.Write($respBytes, 0, $respBytes.Length)
                $whReceived++
            } finally {
                $cli.Close()
            }
        } else {
            Start-Sleep -Milliseconds 100
        }
    }
    $whListener.Stop()
    if ($whReceived -lt 2) { throw "Webhook crash recovery received only $whReceived/2 deliveries" }
    Write-Host ("  [PASS] Crash 3 (znero-webhook): received {0}/2 HMAC webhooks after kill-9 restart" -f $whReceived) -ForegroundColor Green
    & $bbExe -mode=crash-verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-verify (3 webhook) failed" }

    # Crash 4: Customer SMPP server kill -9 -> restart -> ESME reconnects + submitted msgs deliver
    & $bbExe -mode=crash-smpp-send -count=4 -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-smpp-send (4) failed" }
    Restart-ZneroRole -Name 'znero-smpp' -Role 'smpp' -WaitPort 2776
    & $bbExe -mode=crash-verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "crash-verify (4 smpp) failed" }

    # Re-bind ESME to verify SMPP server works cleanly after kill-9 restart
    & $pyExe (Join-Path $PSScriptRoot 'test-smpplib-client.py') --host 127.0.0.1 --port 2776 --system-id $st.cust1_smpp_sys --password $st.cust1_smpp_pass
    if ($global:LASTEXITCODE -ne 0) { throw "ESME reconnect check after znero-smpp restart failed" }

    # Graceful shutdown / drain test
    Write-Host "  [GRACEFUL DRAIN] Sending batch, stopping Znero cleanly, restarting, verifying..." -ForegroundColor Cyan
    & $bbExe -mode=crash-http-send -count=4 -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "graceful drain send failed" }
    & (Join-Path $PSScriptRoot 'dev-down.ps1') -KeepKafka -KeepMocks | Out-Null
    Restart-ZneroRole -Name 'znero-api' -Role 'api' -WaitPort 8080
    Restart-ZneroRole -Name 'znero-smpp' -Role 'smpp' -WaitPort 2776
    Restart-ZneroRole -Name 'znero-dispatcher' -Role 'dispatcher'
    Restart-ZneroRole -Name 'znero-reaper' -Role 'reaper'
    Restart-ZneroRole -Name 'znero-webhook' -Role 'webhook'
    Restart-ZneroRole -Name 'znero-poller' -Role 'poller'
    Start-Sleep -Seconds 6
    & $bbExe -mode=crash-verify -state-file $stateFile -db-url $testDbUrl
    if ($global:LASTEXITCODE -ne 0) { throw "graceful drain crash-verify failed" }

    Write-Host "`n=== PART 2 REAL OS-PROCESS BLACK-BOX E2E: ALL PASSED ===" -ForegroundColor Green
} finally {
    Write-Host "`n[CLEANUP] Stopping Znero + carrier simulators + cleaning temp files..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'dev-down.ps1') -KeepKafka -Quiet | Out-Null
    if ($mockProc -and -not $mockProc.HasExited) { Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue }
    if ($httpProvProc -and -not $httpProvProc.HasExited) { Stop-Process -Id $httpProvProc.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name 'znero-e2ehttpprov','znero-smppmock' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $pyVenv) { Remove-Item -LiteralPath $pyVenv -Recurse -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $stateFile -Force -ErrorAction SilentlyContinue
}

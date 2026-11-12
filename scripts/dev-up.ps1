<#
.SYNOPSIS
  Idempotently builds binaries to bin\ and starts all Znero International services as separate real OS processes:
  Postgres check, Kafka (KRaft), API HTTP server, Customer SMPP server, Dispatcher, Reaper, Webhook Dispatcher,
  HTTP DLR Poller, and Frontend SvelteKit UI. Prints ports and PIDs cleanly.
.PARAMETER Target
  Target database selector: 'test' (default, uses TEST_DATABASE_URL -> znero_test) or 'dev'.
.PARAMETER SkipBuild
  Skip rebuilding bin\znero.exe and bin\znero-smppmock.exe if already built.
.PARAMETER NoFrontend
  Skip launching the SvelteKit frontend dev process (e.g. for backend-only load/black-box runs).
.PARAMETER WithPprof
  Enable loopback-only pprof endpoints on API (:6060), SMPP (:6061), and Dispatcher (:6062).
#>
[CmdletBinding()]
param(
    [ValidateSet('test', 'dev')]
    [string]$Target = 'test',
    [switch]$SkipBuild,
    [switch]$NoFrontend,
    [switch]$WithPprof,
    [switch]$KeepMocks
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script
Ensure-SecretsEncryptionKey
Initialize-RunDir

$binDir = Join-Path $script:RepoRoot 'bin'
if (-not (Test-Path -LiteralPath $binDir)) {
    New-Item -ItemType Directory -Path $binDir | Out-Null
}

# 1. Select target database URL safely (default znero_test)
if ($Target -eq 'test') {
    $targetUrl = Get-Setting 'TEST_DATABASE_URL'
    if ($targetUrl -notmatch '/znero_test([/?]|$)') {
        throw "SAFETY GUARD: -Target test requires TEST_DATABASE_URL targeting znero_test, got $(Mask-ConnectionString $targetUrl)"
    }
    $env:DATABASE_URL = $targetUrl
    $env:DB_NAME = 'znero_test'
    $env:APP_ENV = 'test'
    $env:ALLOW_LOOPBACK_IN_TEST = 'true'
} else {
    $targetUrl = Get-Setting 'DATABASE_URL'
}

$apiHost = Get-Setting 'APP_HOST' '127.0.0.1'
$apiPort = [int](Get-Setting 'APP_PORT' '8080')
$smppAddr = Get-Setting 'CUSTOMER_SMPP_ADDR' "${apiHost}:2776"
$smppPort = [int](($smppAddr -split ':')[-1])
$uiPort  = [int](Get-Setting 'FRONTEND_PORT' '5173')

Write-Host "`n=== Znero dev-up (target=$Target -> $(Mask-ConnectionString $targetUrl)) ===" -ForegroundColor Cyan

# 2. Idempotent cleanup of stale tracked processes and conflicting listeners
$devDown = Join-Path $PSScriptRoot 'dev-down.ps1'
if (Test-Path -LiteralPath $devDown) {
    & $devDown -KeepKafka -KeepMocks:$KeepMocks -Quiet | Out-Null
}

# 3. Verify PostgreSQL
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 5432)) {
    throw "PostgreSQL is not reachable on 127.0.0.1:5432."
}
Write-Host "[OK] PostgreSQL :5432 is reachable" -ForegroundColor Green

# 4. Ensure Apache Kafka (KRaft)
$kafkaBroker = Get-KafkaBroker
if (-not (Test-TcpPort -HostName $kafkaBroker.Host -Port $kafkaBroker.Port)) {
    Write-Host "Starting Kafka (KRaft)..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'start-kafka.ps1')
    if ($global:LASTEXITCODE -ne 0) { throw "Failed to start Kafka." }
}
$kafkaProc = Get-KafkaProcess
$kafkaPid = if ($kafkaProc) { [int]$kafkaProc.ProcessId } else { 0 }
Write-Host "[OK] Kafka $($kafkaBroker.Host):$($kafkaBroker.Port) (PID=$kafkaPid)" -ForegroundColor Green

# 5. Build Go binaries to bin\
$zneroExe = Join-Path $binDir 'znero.exe'
$smppMockExe = Join-Path $binDir 'znero-smppmock.exe'
if (-not $SkipBuild -or -not (Test-Path -LiteralPath $zneroExe) -or -not (Test-Path -LiteralPath $smppMockExe)) {
    Assert-Command 'go' 'Install Go 1.23+.'
    Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
    try {
        Write-Host "Building bin\znero.exe and bin\znero-smppmock.exe..." -ForegroundColor Gray
        & go build -o $zneroExe ./cmd/api
        if ($global:LASTEXITCODE -ne 0) { throw "go build ./cmd/api failed." }
        & go build -o $smppMockExe ./cmd/smppmock
        if ($global:LASTEXITCODE -ne 0) { throw "go build ./cmd/smppmock failed." }
    } finally {
        Pop-Location
    }
}

$tracked = @()

function Start-TrackedComponent {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [string]$WorkingDirectory = $script:RepoRoot,
        [string]$Endpoint = '-'
    )
    $outLog = Join-Path $script:RunDir "$Name.out.log"
    $errLog = Join-Path $script:RunDir "$Name.err.log"
    Remove-Item -LiteralPath $outLog,$errLog -Force -ErrorAction SilentlyContinue

    $proc = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList -WorkingDirectory $WorkingDirectory `
        -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru

    Set-Content -LiteralPath (Join-Path $script:RunDir "$Name.pid") -Value $proc.Id
    $script:tracked += [pscustomobject]@{
        name     = $Name
        pid      = $proc.Id
        endpoint = $Endpoint
        out_log  = $outLog
        err_log  = $errLog
    }
    return $proc
}

$pprofApi  = if ($WithPprof) { @('-pprof-addr=127.0.0.1:6060') } else { @() }
$pprofSmpp = if ($WithPprof) { @('-pprof-addr=127.0.0.1:6061') } else { @() }
$pprofDisp = if ($WithPprof) { @('-pprof-addr=127.0.0.1:6062') } else { @() }

# 6. Start each component as its own real OS process
Start-TrackedComponent -Name 'znero-api'        -FilePath $zneroExe -ArgumentList (@('-role=api') + $pprofApi)        -Endpoint "http://${apiHost}:$apiPort" | Out-Null
Start-TrackedComponent -Name 'znero-smpp'       -FilePath $zneroExe -ArgumentList (@('-role=smpp') + $pprofSmpp)      -Endpoint $smppAddr | Out-Null
Start-TrackedComponent -Name 'znero-dispatcher' -FilePath $zneroExe -ArgumentList (@('-role=dispatcher') + $pprofDisp) -Endpoint 'outbox+smpp-binds' | Out-Null
Start-TrackedComponent -Name 'znero-reaper'     -FilePath $zneroExe -ArgumentList @('-role=reaper')                    -Endpoint 'sweep-worker' | Out-Null
Start-TrackedComponent -Name 'znero-webhook'    -FilePath $zneroExe -ArgumentList @('-role=webhook')                   -Endpoint 'webhook-worker' | Out-Null
Start-TrackedComponent -Name 'znero-poller'     -FilePath $zneroExe -ArgumentList @('-role=poller')                    -Endpoint 'http-dlr-poller' | Out-Null
Start-TrackedComponent -Name 'znero-analytics'  -FilePath $zneroExe -ArgumentList @('-role=analytics')                 -Endpoint 'rollup-worker' | Out-Null

if (-not $NoFrontend) {
    Assert-Command 'node' 'Install Node.js LTS.'
    $feDir = Join-Path $script:RepoRoot 'frontend'
    if (-not (Test-Path -LiteralPath (Join-Path $feDir 'node_modules'))) {
        Push-Location -LiteralPath $feDir
        try { & npm ci } finally { Pop-Location }
    }
    $viteCli = Join-Path $feDir 'node_modules\vite\bin\vite.js'
    if (Test-Path -LiteralPath $viteCli) {
        Start-TrackedComponent -Name 'znero-frontend' -FilePath 'node' -ArgumentList @('node_modules\vite\bin\vite.js', 'dev', '--host', '127.0.0.1', '--port', "$uiPort") -WorkingDirectory $feDir -Endpoint "http://127.0.0.1:$uiPort" | Out-Null
    } else {
        $npmCmd = (Get-Command 'npm.cmd').Source
        Start-TrackedComponent -Name 'znero-frontend' -FilePath $npmCmd -ArgumentList @('run', 'dev') -WorkingDirectory $feDir -Endpoint "http://127.0.0.1:$uiPort" | Out-Null
    }
}

$pidsFile = Join-Path $script:RunDir 'pids.json'
@{
    started_at = (Get-Date).ToString('o')
    db_target  = $Target
    kafka_pid  = $kafkaPid
    processes  = $tracked
} | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $pidsFile

# 7. Wait for API /readyz, SMPP :2776, and Frontend :5173, and verify all worker PIDs stayed alive
$deadline = (Get-Date).AddSeconds(35)
$apiReady = $false
$smppReady = $false
$uiReady = $NoFrontend

while ((Get-Date) -lt $deadline) {
    if (-not $apiReady -and (Test-TcpPort -HostName $apiHost -Port $apiPort)) {
        try {
            $r = Invoke-RestMethod -Uri "http://${apiHost}:$apiPort/readyz" -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($r) { $apiReady = $true }
        } catch {
            if (Test-TcpPort -HostName $apiHost -Port $apiPort) { $apiReady = $true }
        }
    }
    if (-not $smppReady -and (Test-TcpPort -HostName $apiHost -Port $smppPort)) {
        $smppReady = $true
    }
    if (-not $uiReady -and (Test-TcpPort -HostName '127.0.0.1' -Port $uiPort)) {
        $uiReady = $true
    }
    if ($apiReady -and $smppReady -and $uiReady) { break }
    Start-Sleep -Milliseconds 300
}

$dead = @()
foreach ($item in $tracked) {
    $p = Get-Process -Id $item.pid -ErrorAction SilentlyContinue
    if (-not $p -or $p.HasExited) {
        $dead += $item.name
    }
}

Write-Host "`n--- Running Components ---" -ForegroundColor Cyan
Write-Host ("  {0,-18} {1,-8} {2}" -f 'kafka-kraft', $kafkaPid, "$($kafkaBroker.Host):$($kafkaBroker.Port)")
foreach ($item in $tracked) {
    $alive = if ($dead -contains $item.name) { 'EXITED!' } else { 'RUNNING' }
    Write-Host ("  {0,-18} {1,-8} {2,-26} [{3}]" -f $item.name, $item.pid, $item.endpoint, $alive)
}

if (-not $apiReady -or -not $smppReady -or -not $uiReady -or $dead.Count -gt 0) {
    throw "dev-up readiness failed (api=$apiReady, smpp=$smppReady, ui=$uiReady, dead=$($dead -join ','))"
}

Write-Host "`n[READY] All components started cleanly against '$Target'." -ForegroundColor Green

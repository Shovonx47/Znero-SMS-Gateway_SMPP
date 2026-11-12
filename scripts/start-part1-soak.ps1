<#
.SYNOPSIS
  Part 1: Starts the isolated >=120-minute background soak from copied binaries in bin\soak\ against znero_soak.
  Leaves znero_test free for Parts 2-7 and NEVER touches znero_dev.
#>
[CmdletBinding()]
param(
    [string]$Duration = '125m',
    [string]$SampleInterval = '5m',
    [int]$TargetTPS = 50
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script

$testDbUrl = Get-Setting 'TEST_DATABASE_URL'
if ($testDbUrl -notmatch '/znero_test([/?]|$)') {
    throw "SAFETY GUARD: TEST_DATABASE_URL must target znero_test"
}
$soakDbUrl = ($testDbUrl -replace '/znero_test([/?]|$)', '/znero_soak$1')
$adminPgUrl = 'postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable'
$psql = 'C:\Program Files\PostgreSQL\17\bin\psql.exe'

$soakBinDir = Join-Path $script:RepoRoot 'bin\soak'
$soakRunDir = Join-Path $script:RunDir 'soak'
New-Item -ItemType Directory -Force -Path $soakBinDir | Out-Null
New-Item -ItemType Directory -Force -Path $soakRunDir | Out-Null
$pidsFile = Join-Path $soakRunDir 'pids.json'

# Stop any prior soak processes cleanly first
if (Test-Path -LiteralPath $pidsFile) {
    & (Join-Path $PSScriptRoot 'stop-part1-soak.ps1') -Quiet | Out-Null
}

# Build copied binaries strictly inside bin\soak\ so Part 2+ rebuilds in bin\ never disturb the soak
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "[1/5] Building isolated soak binaries into bin\soak\..." -ForegroundColor Cyan
    & go build -o (Join-Path $soakBinDir 'znero-soak.exe') ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-soak.exe failed" }
    & go build -o (Join-Path $soakBinDir 'znero-soakrunner.exe') ./cmd/soakrunner
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero-soakrunner.exe failed" }
} finally {
    Pop-Location
}

Write-Host "[2/5] Recreating clean isolated database znero_soak (znero_dev untouched, znero_test free)..." -ForegroundColor Cyan
$prevPref = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
try {
    & $psql $adminPgUrl -v ON_ERROR_STOP=1 -At -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'znero_soak' AND pid <> pg_backend_pid();" | Out-Null
    & $psql $adminPgUrl -v ON_ERROR_STOP=1 -At -c "DROP DATABASE IF EXISTS znero_soak;" | Out-Null
    if ($global:LASTEXITCODE -ne 0) { throw "DROP DATABASE znero_soak failed" }
    & $psql $adminPgUrl -v ON_ERROR_STOP=1 -At -c "CREATE DATABASE znero_soak OWNER znero_app;" | Out-Null
    if ($global:LASTEXITCODE -ne 0) { throw "CREATE DATABASE znero_soak failed" }
} finally {
    $ErrorActionPreference = $prevPref
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
$prevPref2 = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
try {
    $env:DATABASE_URL = $soakDbUrl
    $env:DB_NAME = 'znero_soak'
    $env:APP_ENV = 'development'
    & migrate -path 'migrations' -database $soakDbUrl up 2>&1 | Write-Host
    if ($global:LASTEXITCODE -ne 0) { throw "migrate up on znero_soak failed" }
    & go run ./cmd/bootstrap -email 'bootstrap-admin@znero.test' -password 'Bootstrap!Pass123' 2>&1 | Out-Null
    if ($global:LASTEXITCODE -ne 0) { throw "bootstrap znero_soak failed" }
    & go run ./cmd/seeddev 2>&1 | Out-Null
    if ($global:LASTEXITCODE -ne 0) { throw "seeddev znero_soak failed" }
    & go run ./cmd/seedmastersample 2>&1 | Out-Null
    if ($global:LASTEXITCODE -ne 0) { throw "seedmastersample znero_soak failed" }
} finally {
    $ErrorActionPreference = $prevPref2
    Pop-Location
}
& $psql $soakDbUrl -v ON_ERROR_STOP=1 -At -c "UPDATE users SET mfa_enabled=false, mfa_secret_encrypted=NULL WHERE email='dev-superadmin@dev.znero.test';" -f (Join-Path $PSScriptRoot 'repair-legacy-rbac-permissions.sql') | Out-Null

Write-Host "[3/5] Ensuring Kafka KRaft :9092 is running..." -ForegroundColor Cyan
& (Join-Path $PSScriptRoot 'start-kafka.ps1') -TimeoutSeconds 35 | Out-Null

Write-Host "[4/5] Launching 6 separate soak service processes (bin\soak\znero-soak.exe)..." -ForegroundColor Cyan
$env:DATABASE_URL = $soakDbUrl
$env:DB_NAME = 'znero_soak'
$env:APP_ENV = 'test'
$env:ALLOW_LOOPBACK_IN_TEST = 'true'
$env:APP_HOST = '127.0.0.1'
$env:APP_PORT = '18180'
$env:CUSTOMER_SMPP_ADDR = '127.0.0.1:12776'
$env:DB_MAX_CONNS = '5'
$env:RATE_LIMIT_MAX = '0'
$env:JWT_ACCESS_DURATION = '4h'
$env:LOG_LEVEL = 'warn'
$env:KAFKA_TEST_TOPIC = 'znero.soak.test'
$env:KAFKA_CONSUMER_GROUP = 'znero-soak'

$roles = @(
    @{ name = 'api';        pprof = '127.0.0.1:16060' },
    @{ name = 'smpp';       pprof = '127.0.0.1:16061' },
    @{ name = 'dispatcher'; pprof = '127.0.0.1:16062' },
    @{ name = 'reaper';     pprof = '127.0.0.1:16063' },
    @{ name = 'webhook';    pprof = '127.0.0.1:16064' },
    @{ name = 'poller';     pprof = '127.0.0.1:16065' }
)

$pids = [ordered]@{}
$soakExe = Join-Path $soakBinDir 'znero-soak.exe'
foreach ($r in $roles) {
    $rn = $r.name
    $outLog = Join-Path $soakRunDir "znero-$rn.out.log"
    $errLog = Join-Path $soakRunDir "znero-$rn.err.log"
    $proc = Start-Process -FilePath $soakExe `
        -ArgumentList @("-role=$rn", "-pprof-addr=$($r.pprof)") `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru
    $pids[$rn] = $proc.Id
    Write-Host ("  Started soak-{0,-10} PID={1} pprof={2}" -f $rn, $proc.Id, $r.pprof) -ForegroundColor Gray
}
$pids | ConvertTo-Json | Set-Content -LiteralPath $pidsFile -Encoding UTF8

# Wait for soak API :18180 and SMPP :12776
$dl = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $dl) {
    if ((Test-TcpPort -Port 18180) -and (Test-TcpPort -Port 12776)) { break }
    Start-Sleep -Milliseconds 250
}
if (-not (Test-TcpPort -Port 18180) -or -not (Test-TcpPort -Port 12776)) {
    throw "Soak API :18180 or SMPP :12776 failed to start"
}

Write-Host "[5/5] Launching znero-soakrunner.exe (duration=$Duration, sample=$SampleInterval, tps=$TargetTPS)..." -ForegroundColor Cyan
$runnerExe = Join-Path $soakBinDir 'znero-soakrunner.exe'
$runnerOut = Join-Path $soakRunDir 'soakrunner.out.log'
$runnerErr = Join-Path $soakRunDir 'soakrunner.err.log'
Remove-Item -LiteralPath $runnerOut -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $runnerErr -Force -ErrorAction SilentlyContinue

$runnerProc = Start-Process -FilePath $runnerExe `
    -ArgumentList @(
        '-api-url', 'http://127.0.0.1:18180',
        '-smpp-addr', '127.0.0.1:12776',
        '-prov-smpp-addr', '127.0.0.1:19200',
        '-prov-http-addr', '127.0.0.1:19202',
        '-webhook-addr', '127.0.0.1:19203',
        '-db-url', $soakDbUrl,
        '-pids-file', $pidsFile,
        '-telemetry-file', (Join-Path $script:RepoRoot 'docs\soak-telemetry.jsonl'),
        '-summary-file', (Join-Path $script:RepoRoot '.tmp\soak_summary.json'),
        '-duration', $Duration,
        '-sample-interval', $SampleInterval,
        '-tps', "$TargetTPS"
    ) `
    -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
    -RedirectStandardOutput $runnerOut -RedirectStandardError $runnerErr -PassThru
$pids['soakrunner'] = $runnerProc.Id
$pids | ConvertTo-Json | Set-Content -LiteralPath $pidsFile -Encoding UTF8

# Wait up to 45s for t=0 baseline sample to appear in telemetry file
$telemetryPath = Join-Path $script:RepoRoot 'docs\soak-telemetry.jsonl'
$dl2 = (Get-Date).AddSeconds(45)
$baselineOK = $false
while ((Get-Date) -lt $dl2) {
    if ($runnerProc.HasExited) {
        $errTxt = Get-Content -LiteralPath $runnerOut,$runnerErr -ErrorAction SilentlyContinue | Out-String
        throw "soakrunner exited early (exit=$($runnerProc.ExitCode)): $errTxt"
    }
    if ((Test-Path -LiteralPath $telemetryPath) -and ((Get-Content -LiteralPath $telemetryPath -ErrorAction SilentlyContinue | Measure-Object).Count -ge 1)) {
        $baselineOK = $true
        break
    }
    Start-Sleep -Milliseconds 500
}
if (-not $baselineOK) {
    throw "Timed out waiting for soak t=0 baseline sample in $telemetryPath"
}

Write-Host "`n[SOAK RUNNING] Isolated >=120m soak active in background! PIDs recorded in $pidsFile" -ForegroundColor Green
Get-Content -LiteralPath $runnerOut -Tail 12

<#
.SYNOPSIS
  Starts the full Phase 9 Visibility Demo Stack pointing to znero_demo.
  Services started:
    1. PostgreSQL (:5432) pointing to znero_demo
    2. Apache Kafka (:9092)
    3. Znero API Server (:8080) with real-time WebSocket live hub
    4. Znero Analytics Rollup Worker (1s interval)
    5. SvelteKit Frontend UI (:5173)
  Prints login credentials and dashboard URLs.
.PARAMETER Reseed
  Re-runs the demo seeding script to refresh all 30-day traffic and rollups.
.PARAMETER NoFrontend
  Launches only backend services (API + Analytics).
#>
[CmdletBinding()]
param(
    [switch]$Reseed,
    [switch]$NoFrontend
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script
Ensure-SecretsEncryptionKey
Initialize-RunDir

$demoDbUrl = "postgres://znero_app:pinhbk0lx2YbUNbxqxnEY7NCUSqUFC8t@localhost:5432/znero_demo?sslmode=disable"
$env:DATABASE_URL = $demoDbUrl
$env:DB_NAME = "znero_demo"
$env:APP_ENV = "development"
$env:RUN_ANALYTICS = "true"
$env:ALLOW_LOOPBACK_IN_TEST = "true"

Write-Host "`n=================================================================" -ForegroundColor Cyan
Write-Host "   ZNERO INTERNATIONAL: PHASE 9 VISIBILITY DEMO STACK (znero_demo)  " -ForegroundColor Cyan
Write-Host "=================================================================" -ForegroundColor Cyan

# 1. Check PostgreSQL
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 5432)) {
    throw "PostgreSQL is not reachable on 127.0.0.1:5432."
}
Write-Host "[OK] PostgreSQL :5432 (znero_demo)" -ForegroundColor Green

# 2. Check/Start Kafka
$kafkaBroker = Get-KafkaBroker
if (-not (Test-TcpPort -HostName $kafkaBroker.Host -Port $kafkaBroker.Port)) {
    Write-Host "Starting Kafka (KRaft)..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'start-kafka.ps1')
}
Write-Host "[OK] Kafka $($kafkaBroker.Host):$($kafkaBroker.Port)" -ForegroundColor Green

# 3. Build Go backend binaries if needed
$binDir = Join-Path $script:RepoRoot 'bin'
if (-not (Test-Path -LiteralPath $binDir)) {
    New-Item -ItemType Directory -Path $binDir | Out-Null
}
$zneroExe = Join-Path $binDir 'znero.exe'
Write-Host "Building bin\znero.exe..." -ForegroundColor Gray
Push-Location (Join-Path $script:RepoRoot 'backend')
try {
    & go build -o $zneroExe ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build ./cmd/api failed." }
} finally {
    Pop-Location
}

# 4. Optional / Initial Seed
if ($Reseed) {
    Write-Host "Reseeding znero_demo database..." -ForegroundColor Yellow
    Push-Location (Join-Path $script:RepoRoot 'backend')
    try {
        & go run ./cmd/seeddemo -db=$demoDbUrl
    } finally {
        Pop-Location
    }
}

# 5. Stop any existing demo processes
$demoDown = Join-Path $PSScriptRoot 'demo-down.ps1'
if (Test-Path -LiteralPath $demoDown) {
    & $demoDown
}

$tracked = @()
function Start-DemoProcess {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [string]$WorkingDirectory = $script:RepoRoot,
        [string]$Endpoint = '-'
    )
    $outLog = Join-Path $script:RunDir "demo-$Name.out.log"
    $errLog = Join-Path $script:RunDir "demo-$Name.err.log"
    Remove-Item -LiteralPath $outLog,$errLog -Force -ErrorAction SilentlyContinue

    $proc = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList -WorkingDirectory $WorkingDirectory `
        -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru

    Set-Content -LiteralPath (Join-Path $script:RunDir "demo-$Name.pid") -Value $proc.Id
    $script:tracked += [pscustomobject]@{
        name     = $Name
        pid      = $proc.Id
        endpoint = $Endpoint
        out_log  = $outLog
        err_log  = $errLog
    }
    return $proc
}

# 6. Start API Server (with Live Hub & REST endpoints)
Start-DemoProcess -Name 'api' -FilePath $zneroExe -ArgumentList @('-role=api') -Endpoint 'http://127.0.0.1:8080' | Out-Null
Write-Host "[OK] Znero API Server running on http://127.0.0.1:8080" -ForegroundColor Green

# 7. Start Analytics Rollup Worker
Start-DemoProcess -Name 'analytics' -FilePath $zneroExe -ArgumentList @('-role=analytics') -Endpoint 'worker:analytics' | Out-Null
Write-Host "[OK] Znero Analytics Rollup Worker running" -ForegroundColor Green

# 8. Start Frontend UI
if (-not $NoFrontend) {
    $frontendDir = Join-Path $script:RepoRoot 'frontend'
    $npmCmd = (Get-Command 'npm.cmd' -ErrorAction SilentlyContinue).Source
    if (-not $npmCmd) { $npmCmd = 'npm' }
    Start-DemoProcess -Name 'frontend' -FilePath $npmCmd -ArgumentList @('run', 'dev', '--', '--host', '127.0.0.1', '--port', '5173') `
        -WorkingDirectory $frontendDir -Endpoint 'http://127.0.0.1:5173' | Out-Null
    Write-Host "[OK] SvelteKit Frontend running on http://127.0.0.1:5173" -ForegroundColor Green
}

# Save tracked services
$servicesFile = Join-Path $script:RunDir 'demo_services.json'
$tracked | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $servicesFile -Encoding UTF8

# 9. Health verification
Write-Host "`nWaiting for API health check..." -NoNewline
$healthy = $false
for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Seconds 1
    try {
        $resp = Invoke-RestMethod -Uri 'http://127.0.0.1:8080/healthz' -TimeoutSec 2 -ErrorAction Stop
        if ($resp -or $resp.success) {
            $healthy = $true
            break
        }
    } catch {
        Write-Host '.' -NoNewline
    }
}
Write-Host ""
if ($healthy) {
    Write-Host "[SUCCESS] All demo services are online and healthy!" -ForegroundColor Green
} else {
    Write-Warning "API is still initializing or responding slowly. Check logs in .run/demo-api.err.log"
}

Write-Host "`n=================================================================" -ForegroundColor Cyan
Write-Host "                DEMO STACK CREDENTIALS & SITEMAP                " -ForegroundColor Cyan
Write-Host "=================================================================" -ForegroundColor Cyan
Write-Host "Frontend Portal:  http://127.0.0.1:5173" -ForegroundColor White
Write-Host "API Endpoint:     http://127.0.0.1:8080" -ForegroundColor White
Write-Host "WebSocket Live:   ws://127.0.0.1:8080/api/v1/ws/live" -ForegroundColor White
Write-Host "`nAvailable User Accounts:" -ForegroundColor Yellow
Write-Host "  1. Platform Super Admin (Full Platform NOC & Carrier Visibility):"
Write-Host "     Email:    admin@znero.internal" -ForegroundColor White
Write-Host "     Password: AdminSecret123!" -ForegroundColor White
Write-Host "  2. Reseller Administrator (Subtree Customer Rollups Only):"
Write-Host "     Email:    admin@apextelecom.net" -ForegroundColor White
Write-Host "     Password: ResellerSecret123!" -ForegroundColor White
Write-Host "  3. Customer Administrator (Direct Customer Metrics & Spend):"
Write-Host "     Email:    dev@fintechpay.com" -ForegroundColor White
Write-Host "     Password: CustomerSecret123!" -ForegroundColor White
Write-Host "`nTo cleanly stop demo services, run:" -ForegroundColor Gray
Write-Host "  .\scripts\demo-down.ps1" -ForegroundColor Gray
Write-Host "=================================================================`n" -ForegroundColor Cyan

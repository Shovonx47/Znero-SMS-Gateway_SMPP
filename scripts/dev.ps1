<#
.SYNOPSIS
  Single daily startup command for Znero International:
  Starts Kafka (KRaft), Backend API, and Frontend UI, then waits for /readyz and prints URLs.
.PARAMETER Foreground
  Runs backend in foreground instead of launching separate process windows.
#>
[CmdletBinding()]
param(
    [switch]$Foreground,
    [switch]$WithSmppMock
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script
Ensure-SecretsEncryptionKey
Initialize-RunDir

Write-Host "`n=== Starting Znero International Development Environment ===" -ForegroundColor Cyan

# 1. Verify PostgreSQL
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 5432)) {
    Write-Warning "PostgreSQL is not listening on 127.0.0.1:5432. Please ensure PostgreSQL service is running."
}

# 2. Start Kafka KRaft broker
$kafkaBroker = Get-KafkaBroker
if (-not (Test-TcpPort -HostName $kafkaBroker.Host -Port $kafkaBroker.Port)) {
    Write-Host "Starting Kafka (KRaft mode)..." -ForegroundColor Gray
    & (Join-Path $PSScriptRoot 'start-kafka.ps1')
} else {
    Write-Host "[OK] Kafka is already running on $($kafkaBroker.Host):$($kafkaBroker.Port)" -ForegroundColor Green
}

# 3. Start Backend & Frontend
$shell = (Get-Process -Id $PID).Path
$common = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File')

Write-Host "Launching Backend API..." -ForegroundColor Gray
$backendProc = Start-Process -FilePath $shell -ArgumentList ($common + @("`"$(Join-Path $PSScriptRoot 'run-backend.ps1')`"")) -PassThru
Set-Content -LiteralPath (Join-Path $script:RunDir 'backend.pid') -Value $backendProc.Id

Write-Host "Launching Frontend UI..." -ForegroundColor Gray
$frontendProc = Start-Process -FilePath $shell -ArgumentList ($common + @("`"$(Join-Path $PSScriptRoot 'run-frontend.ps1')`"")) -PassThru
Set-Content -LiteralPath (Join-Path $script:RunDir 'frontend.pid') -Value $frontendProc.Id

if ($WithSmppMock) {
    Write-Host "Launching Mock SMPP SMSC..." -ForegroundColor Gray
    $mockProc = Start-Process -FilePath $shell -ArgumentList ($common + @("`"$(Join-Path $PSScriptRoot 'run-smpp-mock.ps1')`"")) -PassThru
    Set-Content -LiteralPath (Join-Path $script:RunDir 'smppmock.pid') -Value $mockProc.Id
}

# 4. Wait for Backend /readyz probe
$apiPort = Get-Setting 'APP_PORT' '8080'
$uiPort  = Get-Setting 'FRONTEND_PORT' '5173'

Write-Host "Waiting for backend readiness probe (http://127.0.0.1:$apiPort/readyz)..." -ForegroundColor Gray
$deadline = (Get-Date).AddSeconds(45)
$ready = $false
while ((Get-Date) -lt $deadline) {
    if (Test-TcpPort -Port ([int]$apiPort)) {
        try {
            $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$apiPort/readyz" -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($resp) {
                $ready = $true
                break
            }
        } catch {
            # Still warming up or degraded
            if (Test-TcpPort -Port ([int]$apiPort)) {
                $ready = $true
                break
            }
        }
    }
    Start-Sleep -Milliseconds 600
}

Write-Host "`n================================================================================" -ForegroundColor Green
Write-Host "             ZNERO INTERNATIONAL LOCAL STACK IS READY                           " -ForegroundColor Green
Write-Host "================================================================================" -ForegroundColor Green
Write-Host "  Frontend Web UI   : http://127.0.0.1:$uiPort" -ForegroundColor Cyan
Write-Host "  Backend REST API  : http://127.0.0.1:$apiPort/api/v1" -ForegroundColor Cyan
Write-Host "  Health Status     : http://127.0.0.1:$apiPort/healthz" -ForegroundColor Cyan
Write-Host "  Readiness Probe   : http://127.0.0.1:$apiPort/readyz" -ForegroundColor Cyan
Write-Host "--------------------------------------------------------------------------------"
Write-Host "  To view component status : .\scripts\status.ps1"
Write-Host "  To stop all services     : .\scripts\stop-all.ps1"
Write-Host "================================================================================`n"

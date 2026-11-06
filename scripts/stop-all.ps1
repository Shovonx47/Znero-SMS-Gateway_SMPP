<#
.SYNOPSIS
  Cleanly halts all running Znero International processes (Kafka, backend, and frontend).
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
Initialize-Script

Write-Host "`n=== Stopping All Znero International Services ===" -ForegroundColor Cyan

# 1. Stop Backend
$backendPidFile = Join-Path $script:RunDir 'backend.pid'
if (Test-Path -LiteralPath $backendPidFile) {
    $bPid = Get-Content -LiteralPath $backendPidFile -ErrorAction SilentlyContinue
    if ($bPid) {
        Stop-Process -Id ([int]$bPid) -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $backendPidFile -Force -ErrorAction SilentlyContinue
}
# Also kill any leftover znero-api.exe processes
Get-Process -Name 'znero-api' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Write-Host "[OK] Stopped backend processes" -ForegroundColor Green

# 2. Stop Frontend
$frontendPidFile = Join-Path $script:RunDir 'frontend.pid'
if (Test-Path -LiteralPath $frontendPidFile) {
    $fPid = Get-Content -LiteralPath $frontendPidFile -ErrorAction SilentlyContinue
    if ($fPid) {
        Stop-Process -Id ([int]$fPid) -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $frontendPidFile -Force -ErrorAction SilentlyContinue
}
# Kill any node processes listening on frontend port (5173)
$uiPort = [int](Get-Setting 'FRONTEND_PORT' '5173')
$netstat = netstat -ano | Select-String ":$uiPort\s+.*LISTENING\s+(\d+)"
foreach ($match in $netstat) {
    if ($match.Matches.Groups[1].Value) {
        $pId = [int]$match.Matches.Groups[1].Value
        Stop-Process -Id $pId -Force -ErrorAction SilentlyContinue
    }
}
Write-Host "[OK] Stopped frontend processes" -ForegroundColor Green

# 3. Stop Kafka cleanly
Write-Host "Stopping Kafka broker..." -ForegroundColor Gray
& (Join-Path $PSScriptRoot 'stop-kafka.ps1')

Write-Host "`nAll services stopped successfully." -ForegroundColor Green
& (Join-Path $PSScriptRoot 'status.ps1')

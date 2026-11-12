<#
.SYNOPSIS
  Idempotently stops all Znero International OS processes started by dev-up.ps1
  (API, SMPP server, workers, frontend, mock providers, and optionally Kafka).
.PARAMETER KeepKafka
  Leave the local Kafka KRaft broker running.
.PARAMETER Quiet
  Suppress status banner output (used during idempotent restarts).
#>
[CmdletBinding()]
param(
    [switch]$KeepKafka,
    [switch]$KeepMocks,
    [switch]$Quiet
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script
Initialize-RunDir

function Stop-PidTree {
    param([int]$ProcessId)
    if ($ProcessId -le 0) { return }
    $proc = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
    if (-not $proc) { return }
    if ($script:OnWindows) {
        & taskkill /PID $ProcessId /T /F 2>$null | Out-Null
    } else {
        Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue
    }
}

$pidsFile = Join-Path $script:RunDir 'pids.json'
if (Test-Path -LiteralPath $pidsFile) {
    try {
        $manifest = Get-Content -LiteralPath $pidsFile -Raw | ConvertFrom-Json
        if ($manifest -and $manifest.processes) {
            foreach ($entry in $manifest.processes) {
                if ($entry.pid) {
                    Stop-PidTree -ProcessId ([int]$entry.pid)
                }
            }
        }
    } catch {
        # Best effort fallback to individual pid files below
    }
    Remove-Item -LiteralPath $pidsFile -Force -ErrorAction SilentlyContinue
}

Get-ChildItem -Path $script:RunDir -Filter '*.pid' -ErrorAction SilentlyContinue | ForEach-Object {
    $raw = Get-Content -LiteralPath $_.FullName -ErrorAction SilentlyContinue
    if ($raw -and ($raw.Trim() -match '^\d+$')) {
        Stop-PidTree -ProcessId ([int]$raw.Trim())
    }
    Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue
}

# Clean up any leftover built binaries or port listeners
$killNames = @('znero', 'znero-api')
if (-not $KeepMocks) { $killNames += 'znero-smppmock' }
foreach ($exeName in $killNames) {
    Get-Process -Name $exeName -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

$apiPort  = [int](Get-Setting 'APP_PORT' '8080')
$smppAddr = Get-Setting 'CUSTOMER_SMPP_ADDR' '127.0.0.1:2776'
$smppPort = [int](($smppAddr -split ':')[-1])
$uiPort   = [int](Get-Setting 'FRONTEND_PORT' '5173')

foreach ($port in @($apiPort, $smppPort, $uiPort)) {
    $matches = netstat -ano | Select-String ":$port\s+.*LISTENING\s+(\d+)"
    foreach ($m in $matches) {
        $pId = [int]$m.Matches.Groups[1].Value
        if ($pId -gt 4) {
            Stop-PidTree -ProcessId $pId
        }
    }
}

if (-not $KeepKafka) {
    & (Join-Path $PSScriptRoot 'stop-kafka.ps1') | Out-Null
}

if (-not $Quiet) {
    Write-Host "[OK] Znero processes stopped (KeepKafka=$([bool]$KeepKafka))." -ForegroundColor Green
}


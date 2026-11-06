<#
.SYNOPSIS
  Starts the backend and the frontend, each in its own window (so each has its own logs and Ctrl+C).
.PARAMETER WithKafka
  Also start Kafka first (start-kafka.ps1). Without it the backend still runs; /readyz reports Kafka as down.
#>
[CmdletBinding()]
param(
    [switch]$WithKafka
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script

if ($WithKafka) { & (Join-Path $PSScriptRoot 'start-kafka.ps1') }

$shell = (Get-Process -Id $PID).Path   # powershell.exe on Windows PowerShell, pwsh on PowerShell 7
$common = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-NoExit', '-File')

function Start-InWindow([string]$Script) {
    $argList = $common + @("`"$(Join-Path $PSScriptRoot $Script)`"")
    Start-Process -FilePath $shell -ArgumentList $argList | Out-Null
}

Write-Host 'Starting backend window ...'
Start-InWindow 'run-backend.ps1'
Write-Host 'Starting frontend window ...'
Start-InWindow 'run-frontend.ps1'

$apiPort = Get-Setting 'APP_PORT' '8080'
$uiPort  = Get-Setting 'FRONTEND_PORT' '5173'
Write-Host "Waiting for http://127.0.0.1:$apiPort/healthz ..."
$deadline = (Get-Date).AddSeconds(90)
$ready = $false
while ((Get-Date) -lt $deadline) {
    if (Test-TcpPort -Port ([int]$apiPort)) { $ready = $true; break }
    Start-Sleep -Milliseconds 700
}
if ($ready) { Write-Host "Backend is up:  http://127.0.0.1:$apiPort/healthz" } else { Write-Warning 'Backend did not open its port in 90 s; check its window.' }
Write-Host "Frontend:       http://127.0.0.1:$uiPort/   (first start may take a few seconds)"
Write-Host 'Stop each service with Ctrl+C in its window.'

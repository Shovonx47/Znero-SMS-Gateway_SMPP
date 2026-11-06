<#
.SYNOPSIS
  Reports the live running status of all Znero International system components.
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
Initialize-Script

Write-Host "`n=== Znero International: Component Status Report ===" -ForegroundColor Cyan

# 1. PostgreSQL
$pgUp = Test-TcpPort -HostName '127.0.0.1' -Port 5432
if ($pgUp) {
    Write-Host "  [UP]   PostgreSQL Database   : 127.0.0.1:5432 (Listening)" -ForegroundColor Green
} else {
    Write-Host "  [DOWN] PostgreSQL Database   : 127.0.0.1:5432 (Unreachable)" -ForegroundColor Red
}

# 2. Apache Kafka (KRaft)
$kafkaBroker = Get-KafkaBroker
$kafkaUp = Test-TcpPort -HostName $kafkaBroker.Host -Port $kafkaBroker.Port
$kafkaProc = Get-KafkaProcess
if ($kafkaUp) {
    $pidInfo = if ($kafkaProc) { "PID $($kafkaProc.ProcessId)" } else { "External" }
    Write-Host "  [UP]   Apache Kafka (KRaft)  : $($kafkaBroker.Host):$($kafkaBroker.Port) (Listening, $pidInfo)" -ForegroundColor Green
} else {
    Write-Host "  [DOWN] Apache Kafka (KRaft)  : $($kafkaBroker.Host):$($kafkaBroker.Port) (Not running)" -ForegroundColor Yellow
}

# 3. Backend HTTP API
$apiPort = [int](Get-Setting 'APP_PORT' '8080')
$apiUp = Test-TcpPort -HostName '127.0.0.1' -Port $apiPort
if ($apiUp) {
    Write-Host "  [UP]   Backend HTTP API      : http://127.0.0.1:$apiPort (Port open)" -ForegroundColor Green
    try {
        $ready = Invoke-RestMethod -Uri "http://127.0.0.1:$apiPort/readyz" -TimeoutSec 2 -ErrorAction Stop
        Write-Host "         /readyz Status        : $($ready.data.status)" -ForegroundColor Green
    } catch {
        if ($_.Exception.Response) {
            Write-Host "         /readyz Status        : Degraded (Some dependencies down)" -ForegroundColor Yellow
        }
    }
} else {
    Write-Host "  [DOWN] Backend HTTP API      : http://127.0.0.1:$apiPort (Not running)" -ForegroundColor Red
}

# 4. Frontend UI
$uiPort = [int](Get-Setting 'FRONTEND_PORT' '5173')
$uiUp = Test-TcpPort -HostName '127.0.0.1' -Port $uiPort
if ($uiUp) {
    Write-Host "  [UP]   Frontend SvelteKit UI : http://localhost:$uiPort (Listening)" -ForegroundColor Green
} else {
    Write-Host "  [DOWN] Frontend SvelteKit UI : http://localhost:$uiPort (Not running)" -ForegroundColor Red
}

Write-Host "====================================================`n"

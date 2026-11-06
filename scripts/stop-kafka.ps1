<#
.SYNOPSIS
  Stops the local Kafka broker cleanly (CTRL+C -> JVM shutdown hooks -> controlled shutdown).
.DESCRIPTION
  Never deletes Kafka data. If the broker does not exit within -TimeoutSeconds the script fails
  and leaves it running, unless -Force is given (hard kill; Kafka recovers its logs on next start).
#>
[CmdletBinding()]
param(
    [int]$TimeoutSeconds = 60,
    [switch]$Force
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script

$broker = Get-KafkaBroker
$kafka = Get-KafkaProcess
if (-not $kafka) {
    if (Test-TcpPort -HostName $broker.Host -Port $broker.Port) {
        throw "Port $($broker.Port) is in use but no Kafka JVM was found; refusing to kill an unknown process."
    }
    Write-Host 'Kafka is not running.'
    Remove-Item -LiteralPath (Join-Path $script:RunDir 'kafka.pid') -ErrorAction SilentlyContinue
    exit 0
}

$kafkaPid = [int]$kafka.ProcessId
Write-Host "Stopping Kafka (pid $kafkaPid) gracefully ..."
$hostExe = (Get-Process -Id $PID).Path
$helper = Join-Path $PSScriptRoot '_send-ctrlc.ps1'
$p = Start-Process -FilePath $hostExe -Wait -PassThru -WindowStyle Hidden `
    -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$helper`" -ProcessId $kafkaPid"
if ($p.ExitCode -ne 0) { Write-Warning "Could not deliver CTRL+C (Win32 error $($p.ExitCode))." }

$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
while ((Get-Date) -lt $deadline -and (Get-Process -Id $kafkaPid -ErrorAction SilentlyContinue)) {
    Start-Sleep -Milliseconds 500
}

if (Get-Process -Id $kafkaPid -ErrorAction SilentlyContinue) {
    if (-not $Force) {
        throw "Kafka (pid $kafkaPid) did not shut down within $TimeoutSeconds s and was left running. Re-run with -Force for a hard kill."
    }
    Write-Warning 'Forcing Kafka to stop (hard kill).'
    Stop-Process -Id $kafkaPid -Force
}

Remove-Item -LiteralPath (Join-Path $script:RunDir 'kafka.pid') -ErrorAction SilentlyContinue
Write-Host 'Kafka stopped. Data directory untouched.'
exit 0

<#
.SYNOPSIS
  Starts the local Apache Kafka broker in KRaft mode (no ZooKeeper) as a hidden background process.
.DESCRIPTION
  Reads KAFKA_HOME and KAFKA_BROKERS from .env. Formats the KRaft storage on first run,
  launches the broker JVM directly (no .bat wrapper, so it can later be stopped gracefully by
  stop-kafka.ps1) and waits until the broker port accepts connections.
#>
[CmdletBinding()]
param(
    [int]$TimeoutSeconds = 90
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script

$kafkaHome = Get-Setting 'KAFKA_HOME'
$serverProps = Join-Path (Join-Path (Join-Path $kafkaHome 'config') 'kraft') 'server.properties'
$libs = Join-Path $kafkaHome 'libs'
if (-not (Test-Path -LiteralPath $serverProps)) { throw "KRaft config not found: $serverProps (check KAFKA_HOME in .env)" }

Assert-Command 'java' 'Install a JDK/JRE 17+ and make sure java is on PATH.'
$java = (Get-Command 'java').Source

$broker = Get-KafkaBroker
if (Test-TcpPort -HostName '127.0.0.1' -Port $broker.Port) {
    $existing = Get-KafkaProcess
    $who = if ($existing) { "Kafka (pid $($existing.ProcessId))" } else { 'something' }
    Write-Host "$who is already listening on $($broker.Host):$($broker.Port); nothing to start."
    exit 0
}

# Format the KRaft metadata directory once (idempotent: skipped when meta.properties exists).
$logDirsLine = Get-Content -LiteralPath $serverProps | Where-Object { $_ -match '^\s*log\.dirs\s*=' } | Select-Object -First 1
$logDir = ($logDirsLine -split '=', 2)[1].Trim()
if (-not (Test-Path -LiteralPath (Join-Path $logDir 'meta.properties'))) {
    Write-Host "Formatting KRaft storage at $logDir ..."
    $storageTool = if ($script:OnWindows) { Join-Path (Join-Path (Join-Path $kafkaHome 'bin') 'windows') 'kafka-storage.bat' } else { Join-Path (Join-Path $kafkaHome 'bin') 'kafka-storage.sh' }
    $clusterId = (& $storageTool random-uuid | Select-Object -Last 1).Trim()
    & $storageTool format -t $clusterId -c $serverProps
    if ($LASTEXITCODE -ne 0) { throw 'kafka-storage format failed.' }
}

Initialize-RunDir
$kafkaLogs = Join-Path $kafkaHome 'logs'
if (-not (Test-Path -LiteralPath $kafkaLogs)) { New-Item -ItemType Directory -Path $kafkaLogs | Out-Null }
$log4j = ((Join-Path (Join-Path $kafkaHome 'config') 'log4j.properties') -replace '\\', '/')
$logsProp = ($kafkaLogs -replace '\\', '/')
$classpath = (Join-Path $libs '*')
$outLog = Join-Path $script:RunDir 'kafka.out.log'
$errLog = Join-Path $script:RunDir 'kafka.err.log'

# On Windows, Start-Process with -Redirect* ties the child JVM to the parent PowerShell
# console session: the JVM exits when the spawning shell exits. To get a truly independent
# process we launch java.exe WITHOUT output redirection (-WindowStyle Hidden keeps the
# window hidden). Kafka writes its own logs to $kafkaLogs\server.log via log4j.
$jvmArgs = @(
    '-Xmx1G', '-Xms1G', '-server', '-XX:+UseG1GC', '-XX:MaxGCPauseMillis=20',
    '-XX:InitiatingHeapOccupancyPercent=35', '-XX:+ExplicitGCInvokesConcurrent',
    '-Djava.awt.headless=true',
    "`"-Dkafka.logs.dir=$logsProp`"",
    "`"-Dlog4j.configuration=file:$log4j`"",
    '-cp', "`"$classpath`"",
    'kafka.Kafka', "`"$serverProps`""
) -join ' '

Write-Host "Starting Kafka (KRaft) ..."
$cmd = "`"$java`" $jvmArgs"
$res = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $cmd }
if ($res.ReturnValue -ne 0) { throw "Failed to create Kafka process via WMI (error code $($res.ReturnValue))" }
$procId = [int]$res.ProcessId
Set-Content -LiteralPath (Join-Path $script:RunDir 'kafka.pid') -Value $procId

$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
while ((Get-Date) -lt $deadline) {
    $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
    if (-not $proc) { throw "Kafka exited during startup. See $kafkaLogs\server.log" }
    if (Test-TcpPort -HostName '127.0.0.1' -Port $broker.Port) {
        Write-Host "Kafka is up on 127.0.0.1:$($broker.Port) (pid $procId)."
        exit 0
    }
    Start-Sleep -Milliseconds 500
}
throw "Kafka did not open port $($broker.Port) within $TimeoutSeconds s. See $kafkaLogs\server.log"


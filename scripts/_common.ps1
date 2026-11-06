# Shared helpers for the Znero scripts. Dot-source this file: . "$PSScriptRoot\_common.ps1"
# Every path is built with Join-Path / $PSScriptRoot and always quoted when handed to a native
# command, so a repository path containing spaces (e.g. "Softcents Systems") is safe.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 has no $IsWindows/$IsLinux automatic variables, so test the version first.
$script:OnWindows = ($PSVersionTable.PSVersion.Major -lt 6) -or $IsWindows
$script:RepoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$script:RunDir   = Join-Path $script:RepoRoot '.run'   # pid files and process logs (git-ignored)

function Update-SessionPath {
    # Pick up PATH changes made after this shell started (e.g. tools installed in Phase 0).
    if (-not $script:OnWindows) { return }
    $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $user    = [Environment]::GetEnvironmentVariable('Path', 'User')
    $known   = @($env:PATH -split [IO.Path]::PathSeparator)
    foreach ($entry in (("$machine;$user") -split ';')) {
        if ($entry -and ($known -notcontains $entry)) {
            $env:PATH = "$env:PATH$([IO.Path]::PathSeparator)$entry"
            $known += $entry
        }
    }
}

function Import-DotEnv {
    # Loads KEY=VALUE pairs from the repo-root .env into the process environment.
    # Real environment variables win over the file (same rule as the Go backend).
    param([string]$Path = (Join-Path $script:RepoRoot '.env'))
    if (-not (Test-Path -LiteralPath $Path)) {
        throw ".env not found at '$Path'. Copy .env.example to .env and fill in the values."
    }
    foreach ($line in Get-Content -LiteralPath $Path) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
        $idx = $trimmed.IndexOf('=')
        if ($idx -lt 1) { continue }
        $key = $trimmed.Substring(0, $idx).Trim()
        $val = $trimmed.Substring($idx + 1).Trim()
        if ($val.Length -ge 2 -and (($val.StartsWith('"') -and $val.EndsWith('"')) -or ($val.StartsWith("'") -and $val.EndsWith("'")))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        if (-not [Environment]::GetEnvironmentVariable($key, 'Process')) {
            [Environment]::SetEnvironmentVariable($key, $val, 'Process')
        }
    }
}

function Ensure-SecretsEncryptionKey {
    param([string]$Path = (Join-Path $script:RepoRoot '.env'))
    if (-not (Test-Path -LiteralPath $Path)) { return }
    $content = Get-Content -LiteralPath $Path -Raw
    if (-not $content) { $content = '' }
    $existing = [Environment]::GetEnvironmentVariable('SECRETS_ENCRYPTION_KEY', 'Process')
    $needsGeneration = (-not $existing) -or ($existing -match '^REPLACE_WITH_')
    if ($needsGeneration -and ($content -notmatch '(?m)^SECRETS_ENCRYPTION_KEY=(?!REPLACE_WITH_).+$')) {
        $bytes = New-Object byte[] 32
        $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
        $rng.GetBytes($bytes)
        $b64 = [Convert]::ToBase64String($bytes)
        if ($content -match '(?m)^SECRETS_ENCRYPTION_KEY=.*$') {
            $content = [regex]::Replace($content, '(?m)^SECRETS_ENCRYPTION_KEY=.*$', "SECRETS_ENCRYPTION_KEY=$b64")
        } else {
            $content = $content.TrimEnd() + "`r`nSECRETS_ENCRYPTION_KEY=$b64`r`nSECRETS_KEY_ID=v1`r`n"
        }
        Set-Content -LiteralPath $Path -Value $content -NoNewline
        [Environment]::SetEnvironmentVariable('SECRETS_ENCRYPTION_KEY', $b64, 'Process')
        if (-not [Environment]::GetEnvironmentVariable('SECRETS_KEY_ID', 'Process')) {
            [Environment]::SetEnvironmentVariable('SECRETS_KEY_ID', 'v1', 'Process')
        }
        Write-Host "[OK] Generated SECRETS_ENCRYPTION_KEY (AES-256-GCM) in .env" -ForegroundColor Green
    }
}

function Initialize-Script {
    Update-SessionPath
    Import-DotEnv
    Ensure-SecretsEncryptionKey
}

function Get-Setting {
    param([Parameter(Mandatory)][string]$Name, [string]$Default = $null)
    $value = [Environment]::GetEnvironmentVariable($Name, 'Process')
    if ($value) { return $value }
    if ($null -ne $Default) { return $Default }
    throw "Missing required setting $Name. Set it in .env (see .env.example)."
}

function Assert-Command {
    param([Parameter(Mandatory)][string]$Name, [string]$Hint = '')
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "'$Name' was not found on PATH. $Hint"
    }
}

function Test-TcpPort {
    param([string]$HostName = '127.0.0.1', [Parameter(Mandatory)][int]$Port, [int]$TimeoutMs = 1000)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $task = $client.ConnectAsync($HostName, $Port)
        return ($task.Wait($TimeoutMs) -and $client.Connected)
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

function Get-KafkaBroker {
    # First broker of KAFKA_BROKERS as @{ Host; Port }.
    $first = ((Get-Setting 'KAFKA_BROKERS' 'localhost:9092') -split ',')[0].Trim()
    $parts = $first.Split(':')
    return @{ Host = $parts[0]; Port = [int]$parts[1] }
}

function Get-KafkaProcess {
    # The running Kafka broker JVM (java.exe whose command line contains kafka.Kafka), or $null.
    $procs = Get-CimInstance Win32_Process -Filter "Name = 'java.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match 'kafka\.Kafka' }
    return ($procs | Select-Object -First 1)
}

function Mask-ConnectionString {
    param([string]$Url)
    if (-not $Url) { return $Url }
    # Replace password in postgres://user:password@host with postgres://user:*****@host
    return [regex]::Replace($Url, '(?<=://[^:]+:)[^@]+(?=@)', '*****')
}

function Initialize-RunDir {
    if (-not (Test-Path -LiteralPath $script:RunDir)) {
        New-Item -ItemType Directory -Path $script:RunDir | Out-Null
    }
}


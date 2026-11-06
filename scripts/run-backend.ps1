<#
.SYNOPSIS
  Builds and runs the Go API (backend) in the foreground. Press Ctrl+C for a graceful shutdown.
.DESCRIPTION
  Settings come from .env (the backend reads it itself). The binary is built to .run/ so the
  process receives Ctrl+C directly (go run would sit in between).
#>
[CmdletBinding()]
param()
. "$PSScriptRoot\_common.ps1"
Initialize-Script
Assert-Command 'go' 'Install Go 1.22+ and make sure it is on PATH.'

$backendDir = Join-Path $script:RepoRoot 'backend'
Initialize-RunDir
$exeName = if ($script:OnWindows) { 'znero-api.exe' } else { 'znero-api' }
$exe = Join-Path $script:RunDir $exeName

Push-Location -LiteralPath $backendDir
try {
    Write-Host 'Building backend ...'
    & go build -o $exe ./cmd/api
    if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }
    Write-Host "Starting backend (APP_HOST=$(Get-Setting 'APP_HOST' '127.0.0.1'), APP_PORT=$(Get-Setting 'APP_PORT' '8080')). Ctrl+C to stop."
    & $exe
    exit $LASTEXITCODE
} finally {
    Pop-Location
}

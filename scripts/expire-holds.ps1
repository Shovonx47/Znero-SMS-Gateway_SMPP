# Runs the automated holds expiry job to expire past-due active holds
[CmdletBinding()]
param(
    [string]$DatabaseUrl
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
Initialize-Script

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location (Join-Path $repoRoot "backend")

if (-not $DatabaseUrl) {
    $DatabaseUrl = Get-Setting 'DATABASE_URL'
}

$env:DATABASE_URL = $DatabaseUrl
$maskedUrl = Mask-ConnectionString $DatabaseUrl

Write-Host "Running automated holds expiry job against $maskedUrl..." -ForegroundColor Cyan
& go run ./cmd/expireholds
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}




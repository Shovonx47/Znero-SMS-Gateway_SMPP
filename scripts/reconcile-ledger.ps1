[CmdletBinding()]
param(
    [switch]$Repair,
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

$argsList = @("run", "./cmd/reconcile")
if ($Repair) {
    $argsList += "-repair"
}

Write-Host "Running Ledger Reconciliation against $maskedUrl..." -ForegroundColor Cyan
& go $argsList
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}



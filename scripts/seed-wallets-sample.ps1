<#
.SYNOPSIS
  Seeds clearly labeled fake development sample wallets, ledger top-ups, draft and issued
  invoices, and payment records through the service layer.
  Refuses to run unless APP_ENV=development.
.PARAMETER Cleanup
  Closes DEV SAMPLE wallets, reverses sample transactions, and credits out sample invoices.
#>
[CmdletBinding()]
param(
    [switch]$Cleanup
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

$appEnv = Get-Setting 'APP_ENV'
if ($appEnv -ne 'development') {
    Write-Error "REFUSED: APP_ENV must be 'development'. This wallets sample seed script is strictly prohibited from running in staging or production environments."
    exit 1
}

Assert-Command 'go' 'Install Go 1.22+.'

$argsList = @('run', './cmd/seedwalletssample')
if ($Cleanup) {
    $argsList += @('-cleanup')
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "Running wallets & billing sample seed tool (Cleanup: $Cleanup)..." -ForegroundColor Cyan
    & go @argsList
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}


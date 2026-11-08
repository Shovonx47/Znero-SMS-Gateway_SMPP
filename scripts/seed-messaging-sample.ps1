<#
.SYNOPSIS
  Seeds clearly labeled fake development sample messaging connection (pointing to mock SMSC on loopback),
  route plan, rate cards, and customer funds through the service layer.
  Refuses to run unless APP_ENV=development.
.PARAMETER Cleanup
  Removes or disables sample messaging configurations without ever deleting ledger rows.
#>
[CmdletBinding()]
param(
    [switch]$Cleanup
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

$appEnv = Get-Setting 'APP_ENV'
if ($appEnv -ne 'development') {
    Write-Error "REFUSED: APP_ENV must be 'development'. This messaging sample seed script is strictly prohibited from running in staging or production environments."
    exit 1
}

Assert-Command 'go' 'Install Go 1.22+.'

$argsList = @('run', './cmd/seedmessagingsample')
if ($Cleanup) {
    $argsList += @('-cleanup')
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "Running messaging sample seed tool (Cleanup: $Cleanup)..." -ForegroundColor Cyan
    & go @argsList
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}


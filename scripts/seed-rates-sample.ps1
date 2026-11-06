<#
.SYNOPSIS
  Seeds clearly labeled fake development sample rate cards, buy & sell rates, customer
  rate assignments, reseller cost assignments, and upcoming rate changes through the service layer.
  Refuses to run unless APP_ENV=development.
.PARAMETER Cleanup
  Removes all fake development sample rate cards, rates, and assignments.
#>
[CmdletBinding()]
param(
    [switch]$Cleanup
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

$appEnv = Get-Setting 'APP_ENV'
if ($appEnv -ne 'development') {
    Write-Error "REFUSED: APP_ENV must be 'development'. This rates sample seed script is strictly prohibited from running in staging or production environments."
    exit 1
}

Assert-Command 'go' 'Install Go 1.22+.'

$argsList = @('run', './cmd/seedratessample')
if ($Cleanup) {
    $argsList += @('-cleanup')
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "Running rates sample seed tool (Cleanup: $Cleanup)..." -ForegroundColor Cyan
    & go @argsList
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}

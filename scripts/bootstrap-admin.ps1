<#
.SYNOPSIS
  Bootstraps the initial platform Super Admin user and platform organization.
.PARAMETER Email
  The Super Admin email address.
.PARAMETER Password
  The Super Admin password (minimum 12 characters, non-common).
.PARAMETER NonInteractive
  Fails if email or password are not provided via arguments.
#>
[CmdletBinding()]
param(
    [string]$Email = '',
    [string]$Password = '',
    [switch]$NonInteractive
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

Assert-Command 'go' 'Install Go 1.22+.'

$argsList = @('run', './cmd/bootstrap')
if ($Email) {
    $argsList += @('-email', $Email)
}
if ($Password) {
    $argsList += @('-password', $Password)
}
if ($NonInteractive) {
    $argsList += @('-non-interactive')
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    & go @argsList
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}

<#
.SYNOPSIS
  Applies all pending database migrations (golang-migrate) to the database in DATABASE_URL.
#>
[CmdletBinding()]
param()
. "$PSScriptRoot\_common.ps1"
Initialize-Script
Assert-Command 'migrate' 'Install golang-migrate (see docs/ARCHITECTURE.md) and put it on PATH.'
$dbUrl = Get-Setting 'DATABASE_URL'

# Run from /backend with a RELATIVE migrations path: avoids Windows drive-letter/space issues in file:// URLs.
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    & migrate -path 'migrations' -database $dbUrl up
    if ($LASTEXITCODE -ne 0) { throw "migrate up failed (exit $LASTEXITCODE)." }
    Write-Host 'Current migration version:'
    & migrate -path 'migrations' -database $dbUrl version
} finally {
    Pop-Location
}

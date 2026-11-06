<#
.SYNOPSIS
  Rolls database migrations back. Rolls back ONE migration by default.
.PARAMETER Steps
  Number of migrations to roll back (default 1).
.PARAMETER All
  Roll back every migration (drops everything the migrations created).
.PARAMETER Yes
  Skip the confirmation prompt.
#>
[CmdletBinding()]
param(
    [ValidateRange(1, 1000)][int]$Steps = 1,
    [switch]$All,
    [switch]$Yes
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script
Assert-Command 'migrate' 'Install golang-migrate (see docs/ARCHITECTURE.md) and put it on PATH.'
$dbUrl = Get-Setting 'DATABASE_URL'

$what = if ($All) { 'ALL migrations' } else { "the last $Steps migration(s)" }
if (-not $Yes) {
    $answer = Read-Host "This will roll back $what on the database from DATABASE_URL. Type 'yes' to continue"
    if ($answer -ne 'yes') { Write-Host 'Aborted.'; exit 1 }
}

Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    if ($All) {
        & migrate -path 'migrations' -database $dbUrl down -all
    } else {
        & migrate -path 'migrations' -database $dbUrl down $Steps
    }
    if ($LASTEXITCODE -ne 0) { throw "migrate down failed (exit $LASTEXITCODE)." }
    Write-Host 'Current migration version:'
    # After rolling back everything, "version" reports "no migration" and exits non-zero: that is expected.
    $ErrorActionPreference = 'Continue'
    & migrate -path 'migrations' -database $dbUrl version
    $global:LASTEXITCODE = 0
} finally {
    Pop-Location
}
exit 0

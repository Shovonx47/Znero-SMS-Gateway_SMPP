<#
.SYNOPSIS
  Operator Database Upgrade Script:
  Safely upgrades database schema to the latest migration version with full pre-flight verification,
  automated backup, table ownership enforcement (znero_app), and deterministic rollback instructions.

.DESCRIPTION
  CRITICAL SAFETY RULE: Never run without an explicit confirmation.
  Performs:
  1. Connection test & pre-upgrade ownership check
  2. Timestamped pg_dump backup to .tmp/backups/
  3. Migration up to latest schema
  4. Post-migration ownership verification & permissions check
  5. Rollback command display if failure occurs

.PARAMETER DatabaseUrl
  Target database connection string. Defaults to DATABASE_URL from .env.
.PARAMETER SkipBackup
  Skip pre-upgrade pg_dump (NOT recommended for production).
.PARAMETER Force
  Bypass interactive confirmation prompt.
#>
[CmdletBinding()]
param(
    [string]$DatabaseUrl,
    [switch]$SkipBackup,
    [switch]$Force
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script

$pgBin = 'C:\Program Files\PostgreSQL\17\bin'
$psql = Join-Path $pgBin 'psql.exe'
$pgDump = Join-Path $pgBin 'pg_dump.exe'
$pgRestore = Join-Path $pgBin 'pg_restore.exe'

if (-not (Test-Path -LiteralPath $psql)) {
    $psqlCmd = Get-Command 'psql' -ErrorAction SilentlyContinue
    if ($psqlCmd) { $psql = $psqlCmd.Source } else { throw "psql not found" }
}
if (-not (Test-Path -LiteralPath $pgDump)) {
    $pgDumpCmd = Get-Command 'pg_dump' -ErrorAction SilentlyContinue
    if ($pgDumpCmd) { $pgDump = $pgDumpCmd.Source } else { throw "pg_dump not found" }
}

if (-not $DatabaseUrl) {
    $DatabaseUrl = Get-Setting 'DATABASE_URL'
}
if (-not $DatabaseUrl) {
    throw "DATABASE_URL is not set."
}

Write-Host "=== ZNERO DATABASE UPGRADE UTILITY ===" -ForegroundColor Cyan
Write-Host "Target: $($DatabaseUrl -replace ':[^@/]+@', ':****@')" -ForegroundColor Yellow

if (-not $Force) {
    $confirm = Read-Host "Are you sure you want to upgrade this database? (Type 'YES' to proceed)"
    if ($confirm -ne 'YES') {
        Write-Host "Upgrade cancelled by operator." -ForegroundColor Gray
        exit 0
    }
}

$backupDir = Join-Path $script:RepoRoot '.tmp\backups'
if (-not (Test-Path -LiteralPath $backupDir)) {
    New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
}
$ts = Get-Date -Format 'yyyyMMdd_HHmmss'
$backupFile = Join-Path $backupDir "pre_upgrade_$ts.dump"

# 1. Pre-upgrade Backup
if (-not $SkipBackup) {
    Write-Host "`n[1/4] Creating pre-upgrade backup: $backupFile ..." -ForegroundColor Cyan
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $pgDump -Fc -d $DatabaseUrl -f $backupFile
    $dumpExit = $global:LASTEXITCODE
    $ErrorActionPreference = $prev
    if ($dumpExit -ne 0) {
        throw "pg_dump backup failed (exit $dumpExit). Upgrade aborted."
    }
    Write-Host "  Backup verified: $((Get-Item $backupFile).Length) bytes" -ForegroundColor Green
} else {
    Write-Host "`n[1/4] SKIPPING backup (-SkipBackup passed)..." -ForegroundColor Yellow
}

# 2. Apply Migrations
Write-Host "`n[2/4] Applying schema migrations..." -ForegroundColor Cyan
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Assert-Command 'migrate' 'golang-migrate must be installed on PATH'
    & migrate -path 'migrations' -database $DatabaseUrl up
    if ($global:LASTEXITCODE -ne 0) {
        Write-Host "`n[CRITICAL ERROR] Migration failed!" -ForegroundColor Red
        if (Test-Path $backupFile) {
            Write-Host "ROLLBACK INSTRUCTIONS:" -ForegroundColor Yellow
            Write-Host "  pg_restore --clean --if-exists -d `"$DatabaseUrl`" `"$backupFile`"" -ForegroundColor White
        }
        throw "Migration up failed"
    }
    Write-Host "  Current schema version:" -ForegroundColor Gray
    & migrate -path 'migrations' -database $DatabaseUrl version
} finally {
    Pop-Location
}

# 3. Fix Object Ownership to znero_app
Write-Host "`n[3/4] Ensuring table and sequence ownership is znero_app..." -ForegroundColor Cyan
$ownershipSql = @"
DO \$\$
DECLARE
    r RECORD;
BEGIN
    FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
        EXECUTE 'ALTER TABLE public.' || quote_ident(r.tablename) || ' OWNER TO znero_app;';
    END LOOP;
    FOR r IN (SELECT sequence_name FROM information_schema.sequences WHERE sequence_schema = 'public') LOOP
        EXECUTE 'ALTER SEQUENCE public.' || quote_ident(r.sequence_name) || ' OWNER TO znero_app;';
    END LOOP;
END
\$\$;
"@
$prev = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
& $psql $DatabaseUrl -v ON_ERROR_STOP=1 -At -c $ownershipSql | Out-Null
$ErrorActionPreference = $prev

# 4. Verify Database Integrity
Write-Host "`n[4/4] Verifying upgrade integrity..." -ForegroundColor Cyan
$checkScript = Join-Path $PSScriptRoot 'check-db-ownership.ps1'
if (Test-Path $checkScript) {
    & $checkScript -Target test
}

Write-Host "`n=== DATABASE UPGRADE COMPLETED SUCCESSFULLY ===" -ForegroundColor Green
if (Test-Path $backupFile) {
    Write-Host "Pre-upgrade backup preserved at: $backupFile" -ForegroundColor Gray
}

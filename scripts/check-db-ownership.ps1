<#
.SYNOPSIS
  Read-only check of schema migration state and table/sequence ownership (must be 'znero_app')
  on znero_test and/or znero_dev. Never modifies any database object.
.PARAMETER Target
  Which database(s) to inspect: 'test', 'dev', or 'all' (default: 'test').
.PARAMETER FailOnMismatch
  Exit non-zero if any object is not owned by znero_app or migration state is incomplete/dirty.
#>
[CmdletBinding()]
param(
    [ValidateSet('test', 'dev', 'all')]
    [string]$Target = 'test',
    [switch]$FailOnMismatch
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script

function Resolve-PsqlExe {
    $cmd = Get-Command 'psql.exe' -ErrorAction SilentlyContinue | Where-Object { (Get-Item $_.Source).Length -gt 0 } | Select-Object -First 1
    if ($cmd) { return $cmd.Source }
    $pgExe = Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\psql.exe' -ErrorAction SilentlyContinue | Sort-Object FullName -Descending | Select-Object -First 1
    if ($pgExe) { return $pgExe.FullName }
    throw 'Real psql.exe was not found. Install PostgreSQL client tools or add PostgreSQL bin directory to PATH.'
}
$script:PsqlExe = Resolve-PsqlExe

function Invoke-OwnershipReport {
    param([Parameter(Mandatory)][string]$Label, [Parameter(Mandatory)][string]$DbUrl)

    Write-Host "`n=== DB Ownership & Migration Check: $Label ($(Mask-ConnectionString $DbUrl)) ===" -ForegroundColor Cyan

    $migOut = & $script:PsqlExe -d $DbUrl -At -F '|' -c "SELECT version, dirty FROM schema_migrations;" 2>&1
    if ($global:LASTEXITCODE -ne 0 -or -not ($migOut -join '').Trim()) {
        Write-Warning "Failed querying schema_migrations on $Label : $migOut"
        return $false
    }
    $migStr = ($migOut | Where-Object { $_ -and $_.Trim() -ne '' } | Select-Object -First 1).Trim()
    Write-Host "  Migration state (version|dirty): $migStr" -ForegroundColor White

    $ownSql = @"
SELECT c.relkind, c.relname, pg_get_userbyid(c.relowner) AS owner
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r','p','S','v','m')
  AND pg_get_userbyid(c.relowner) <> 'znero_app'
ORDER BY c.relkind, c.relname;
"@
    $unowned = & $script:PsqlExe -d $DbUrl -At -F '|' -c $ownSql 2>&1
    if ($global:LASTEXITCODE -ne 0) {
        Write-Warning "Failed querying pg_class ownership on $Label : $unowned"
        return $false
    }

    $privSql = @"
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r','p')
  AND NOT has_table_privilege('znero_app', c.oid, 'SELECT,INSERT,UPDATE,DELETE')
ORDER BY c.relname;
"@
    $missingPriv = & $script:PsqlExe -d $DbUrl -At -c $privSql 2>&1
    if ($global:LASTEXITCODE -ne 0) {
        Write-Warning "Failed querying table privileges on $Label : $missingPriv"
        return $false
    }

    $unownedLines = @($unowned | Where-Object { $_ -and $_.Trim() -ne '' })
    $privLines    = @($missingPriv | Where-Object { $_ -and $_.Trim() -ne '' })

    if ($unownedLines.Count -eq 0) {
        Write-Host "  [OK] All public tables/sequences/views are owned by 'znero_app'." -ForegroundColor Green
    } else {
        Write-Host "  [WARN] $($unownedLines.Count) object(s) NOT owned by 'znero_app':" -ForegroundColor Yellow
        foreach ($line in $unownedLines) { Write-Host "    - $line" -ForegroundColor Yellow }
    }

    if ($privLines.Count -eq 0) {
        Write-Host "  [OK] 'znero_app' has full DML privileges on all public tables." -ForegroundColor Green
    } else {
        Write-Host "  [WARN] 'znero_app' lacks SELECT/INSERT/UPDATE/DELETE on:" -ForegroundColor Red
        foreach ($tbl in $privLines) { Write-Host "    - $tbl" -ForegroundColor Red }
    }

    $parts = ($migStr -split '\|')
    $verOk = ($parts.Count -ge 2) -and ([int]$parts[0] -ge 14) -and ($parts[1] -eq 'f')
    return ($verOk -and $unownedLines.Count -eq 0 -and $privLines.Count -eq 0)
}

$allOk = $true
if ($Target -eq 'test' -or $Target -eq 'all') {
    $testUrl = Get-Setting 'TEST_DATABASE_URL'
    $ok = Invoke-OwnershipReport -Label 'znero_test' -DbUrl $testUrl
    if (-not $ok) { $allOk = $false }
}
if ($Target -eq 'dev' -or $Target -eq 'all') {
    $devUrl = Get-Setting 'DATABASE_URL'
    $ok = Invoke-OwnershipReport -Label 'znero_dev (READ-ONLY)' -DbUrl $devUrl
    if (-not $ok) { $allOk = $false }
}

if ($FailOnMismatch -and -not $allOk) {
    exit 1
}
exit 0

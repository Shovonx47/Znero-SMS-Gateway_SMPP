<#
.SYNOPSIS
  Part 9 Database Migration (14 -> 1 -> 14 on populated data) and Backup/Restore Verification (znero_test -> znero_restore_tmp).
  NEVER touches znero_dev or PostgreSQL server configuration.
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script
$env:Path = "C:\msys64\ucrt64\bin;" + $env:Path
$env:CGO_ENABLED = "1"

$testDbUrl = Get-Setting 'TEST_DATABASE_URL'
if ($testDbUrl -notmatch '/znero_test([/?]|$)') {
    throw "SAFETY GUARD: TEST_DATABASE_URL must target znero_test"
}
$restoreDbUrl = ($testDbUrl -replace '/znero_test([/?]|$)', '/znero_restore_tmp$1')

$pgBin = 'C:\Program Files\PostgreSQL\17\bin'
$psql = Join-Path $pgBin 'psql.exe'
$pgDump = Join-Path $pgBin 'pg_dump.exe'
$pgRestore = Join-Path $pgBin 'pg_restore.exe'
foreach ($tool in @($psql, $pgDump, $pgRestore)) {
    if (-not (Test-Path -LiteralPath $tool)) { throw "Missing PostgreSQL tool: $tool" }
}
Assert-Command 'migrate' 'Install golang-migrate'

$tmpDir = Join-Path $script:RepoRoot '.tmp'
if (-not (Test-Path -LiteralPath $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir | Out-Null }
$dumpFile = Join-Path $tmpDir 'znero_test_part9.dump'
$binDir = Join-Path $script:RepoRoot 'bin'

function Invoke-PSQL {
    param([string]$Url, [string]$Sql)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $out = & $psql $Url -v ON_ERROR_STOP=1 -At -c $Sql 2>&1
        if ($global:LASTEXITCODE -ne 0) { throw "psql failed ($Url): $out" }
        return ($out | Out-String).Trim()
    } finally {
        $ErrorActionPreference = $prev
    }
}

Write-Host "=== Part 9 DB Verification: Migration 14->1->14 + pg_dump/pg_restore ===" -ForegroundColor Cyan

# 1. Confirm znero_test is populated pre-rollback
$preOrgs = [int](Invoke-PSQL -Url $testDbUrl -Sql "SELECT COUNT(*) FROM organizations;")
$preMsgs = [int](Invoke-PSQL -Url $testDbUrl -Sql "SELECT COUNT(*) FROM messages;")
$preCRs  = [int](Invoke-PSQL -Url $testDbUrl -Sql "SELECT COUNT(*) FROM change_requests;")
Write-Host ("[1/5] Pre-rollback znero_test populated counts: orgs={0} messages={1} change_requests={2}" -f $preOrgs, $preMsgs, $preCRs) -ForegroundColor Gray
if ($preOrgs -lt 2) {
    throw "Expected populated znero_test before rollback test (orgs=$preOrgs msgs=$preMsgs)"
}

# 2. Rollback 14 -> 1 cleanly on POPULATED znero_test (BUG-038 regression)
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    Write-Host "[2/5] Rolling back migrations 14 -> 1 on populated znero_test..." -ForegroundColor Cyan
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & migrate -path 'migrations' -database $testDbUrl down 13 2>&1 | Write-Host
    $downRc = $global:LASTEXITCODE
    if ($downRc -ne 0) { throw "migrate down 13 failed (exit=$downRc)" }
    $verDown = (& migrate -path 'migrations' -database $testDbUrl version 2>&1 | Out-String).Trim()
    $ErrorActionPreference = $prev
    Write-Host "  After down 13: version=$verDown" -ForegroundColor Green

    Write-Host "[3/5] Reapplying migrations 1 -> 14 on znero_test..." -ForegroundColor Cyan
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & migrate -path 'migrations' -database $testDbUrl up 2>&1 | Write-Host
    $upRc = $global:LASTEXITCODE
    $ErrorActionPreference = $prev
    if ($upRc -ne 0) { throw "migrate up failed (exit=$upRc)" }
    $verUp = Invoke-PSQL -Url $testDbUrl -Sql "SELECT version || '|' || dirty FROM schema_migrations;"
    Write-Host "  After reapply up: schema_migrations=$verUp" -ForegroundColor Green
    if ($verUp -ne '14|false' -and $verUp -ne '14|f') { throw "Unexpected schema_migrations after reapply: $verUp" }
} finally {
    Pop-Location
}

# 3. Re-bootstrap & re-seed clean sample baseline into znero_test + verify ownership
Write-Host "[4/5] Re-seeding clean znero_test baseline + verifying ownership..." -ForegroundColor Cyan
$env:DATABASE_URL = $testDbUrl
$env:DB_NAME = 'znero_test'
$env:APP_ENV = 'development'
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    & go run ./cmd/bootstrap -email 'bootstrap-admin@znero.test' -password 'Bootstrap!Pass123'
    if ($global:LASTEXITCODE -ne 0) { throw "bootstrap failed" }
    foreach ($seed in @('seeddev', 'seedmastersample', 'seedproviderssample', 'seedratessample', 'seedwalletssample', 'seedmessagingsample')) {
        & go run "./cmd/$seed"
        if ($global:LASTEXITCODE -ne 0) { throw "go run ./cmd/$seed failed" }
    }
    # Ensure dev-superadmin login works without forced MFA and repair legacy RBAC grants
    $null = Invoke-PSQL -Url $testDbUrl -Sql "UPDATE users SET mfa_enabled = false, mfa_secret_encrypted = NULL WHERE email = 'dev-superadmin@dev.znero.test';"
    $null = & $psql $testDbUrl -v ON_ERROR_STOP=1 -At -f (Join-Path $PSScriptRoot 'repair-legacy-rbac-permissions.sql')
    & go build -o (Join-Path $binDir 'znero.exe') ./cmd/api
    if ($global:LASTEXITCODE -ne 0) { throw "go build znero.exe failed" }
} finally {
    Pop-Location
}
& (Join-Path $PSScriptRoot 'check-db-ownership.ps1') -Target test -FailOnMismatch
if ($global:LASTEXITCODE -ne 0) { throw "check-db-ownership failed" }

# 4. pg_dump znero_test -> restore into fresh temporary DB znero_restore_tmp -> smoke + reconciliation -> DROP DATABASE
Write-Host "[5/5] Testing pg_dump -> znero_restore_tmp restore, ledger audit, and live API smoke test..." -ForegroundColor Cyan
$apiProc = $null
try {
    Remove-Item -LiteralPath $dumpFile -Force -ErrorAction SilentlyContinue
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $pgDump $testDbUrl --no-owner --format=custom -f $dumpFile 2>&1 | Write-Host
    $dumpRc = $global:LASTEXITCODE
    $ErrorActionPreference = $prev
    if ($dumpRc -ne 0) { throw "pg_dump failed (exit=$dumpRc)" }
    $dumpKB = [math]::Round((Get-Item -LiteralPath $dumpFile).Length / 1KB, 1)
    Write-Host "  pg_dump created $dumpFile ($dumpKB KB)" -ForegroundColor Gray

    $adminPgUrl = 'postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable'
    $null = Invoke-PSQL -Url $adminPgUrl -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'znero_restore_tmp' AND pid <> pg_backend_pid();"
    $null = Invoke-PSQL -Url $adminPgUrl -Sql "DROP DATABASE IF EXISTS znero_restore_tmp;"
    $null = Invoke-PSQL -Url $adminPgUrl -Sql "CREATE DATABASE znero_restore_tmp OWNER znero_app;"

    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $pgRestore --no-owner --role=znero_app -d $restoreDbUrl $dumpFile 2>&1 | Write-Host
    $rstRc = $global:LASTEXITCODE
    $ErrorActionPreference = $prev
    if ($rstRc -ne 0) { throw "pg_restore failed (exit=$rstRc)" }

    $rstVer = Invoke-PSQL -Url $restoreDbUrl -Sql "SELECT version || '|' || dirty FROM schema_migrations;"
    $nonApp = [int](Invoke-PSQL -Url $restoreDbUrl -Sql "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public' AND tableowner <> 'znero_app';")
    $srcCnt = Invoke-PSQL -Url $testDbUrl -Sql "SELECT (SELECT COUNT(*) FROM organizations)||','||(SELECT COUNT(*) FROM users)||','||(SELECT COUNT(*) FROM messages)||','||(SELECT COUNT(*) FROM ledger_transactions);"
    $rstCnt = Invoke-PSQL -Url $restoreDbUrl -Sql "SELECT (SELECT COUNT(*) FROM organizations)||','||(SELECT COUNT(*) FROM users)||','||(SELECT COUNT(*) FROM messages)||','||(SELECT COUNT(*) FROM ledger_transactions);"
    $unbal  = [int](Invoke-PSQL -Url $restoreDbUrl -Sql "SELECT COUNT(*) FROM (SELECT transaction_id FROM ledger_entries GROUP BY transaction_id HAVING COALESCE(SUM(CASE WHEN direction='debit' THEN amount ELSE 0 END),0) <> COALESCE(SUM(CASE WHEN direction='credit' THEN amount ELSE 0 END),0)) t;")
    Write-Host ("  Restored DB: schema={0} non_znero_app_tables={1} counts(src={2}, restored={3}) unbalanced_ledger_txs={4}" -f $rstVer, $nonApp, $srcCnt, $rstCnt, $unbal) -ForegroundColor Green
    if (($rstVer -ne '14|false' -and $rstVer -ne '14|f') -or $nonApp -ne 0 -or $srcCnt -ne $rstCnt -or $unbal -ne 0) {
        throw "Restored database integrity check failed!"
    }

    # Ensure Kafka running for /readyz check on restored DB
    & (Join-Path $PSScriptRoot 'start-kafka.ps1') -TimeoutSeconds 30 | Out-Null

    # Smoke-test a live znero-api process against znero_restore_tmp on :18090
    $env:DATABASE_URL = $restoreDbUrl
    $env:DB_NAME = 'znero_restore_tmp'
    $env:APP_ENV = 'test'
    $env:ALLOW_LOOPBACK_IN_TEST = 'true'
    $env:APP_PORT = '18090'
    $env:APP_HOST = '127.0.0.1'
    $apiProc = Start-Process -FilePath (Join-Path $binDir 'znero.exe') -ArgumentList @('-role=api') `
        -WorkingDirectory $script:RepoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $script:RunDir 'restore-smoke.out.log') `
        -RedirectStandardError (Join-Path $script:RunDir 'restore-smoke.err.log') -PassThru

    $dl = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $dl) {
        if (Test-TcpPort -HostName '127.0.0.1' -Port 18090) { break }
        Start-Sleep -Milliseconds 250
    }
    if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 18090)) { throw "Restore smoke API failed to open :18090" }

    $hz = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18090/healthz'
    $rz = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18090/readyz'
    $reqIdHdr = $hz.Headers['X-Request-ID']
    if ($hz.StatusCode -ne 200 -or $rz.StatusCode -ne 200 -or -not $reqIdHdr) {
        throw "Restore smoke probe failed: hz=$($hz.StatusCode) rz=$($rz.StatusCode) req_id=$reqIdHdr"
    }

    $loginRes = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:18090/api/v1/auth/login' -ContentType 'application/json' `
        -Body '{"email":"dev-superadmin@dev.znero.test","password":"DevSeed1234!#"}'
    $tok = $loginRes.data.access_token
    if (-not $tok) { throw "Restore smoke login returned no token" }

    $recRes = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:18090/api/v1/messaging/reconciliation/run' `
        -Headers @{ Authorization = "Bearer $tok" } -ContentType 'application/json' -Body '{}'
    $recData = if ($recRes.data) { $recRes.data } else { $recRes }
    Write-Host ("  [PASS] Restored DB live API smoke: /healthz=200 /readyz=200 X-Request-ID={0} reconcile(disc={1}, ledger_matches={2})" -f $reqIdHdr, $recData.discrepancy_count, $recData.ledger_matches) -ForegroundColor Green
    if ([int]$recData.discrepancy_count -ne 0 -or -not $recData.ledger_matches) {
        throw "Restored DB reconciliation failed!"
    }
} finally {
    if ($apiProc -and -not $apiProc.HasExited) {
        Stop-Process -Id $apiProc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 300
    }
    $adminPgUrl = 'postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable'
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $psql $adminPgUrl -At -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'znero_restore_tmp' AND pid <> pg_backend_pid();" 2>&1 | Out-Null
    & $psql $adminPgUrl -At -c "DROP DATABASE IF EXISTS znero_restore_tmp;" 2>&1 | Out-Null
    $ErrorActionPreference = $prev
    Remove-Item -LiteralPath $dumpFile -Force -ErrorAction SilentlyContinue
}

Write-Host "`n=== PART 9 DB MIGRATION & BACKUP/RESTORE VERIFICATION: ALL PASSED ===" -ForegroundColor Green

<#
.SYNOPSIS
  Checks PostgreSQL, ensures the application user and database exist, and applies all pending migrations.
  Safe to run repeatedly.
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
Initialize-Script
Ensure-SecretsEncryptionKey

Write-Host "== PostgreSQL & Database Setup ==" -ForegroundColor Cyan

# 1. Check if PostgreSQL port is open
if (-not (Test-TcpPort -HostName '127.0.0.1' -Port 5432)) {
    throw "PostgreSQL is not reachable on 127.0.0.1:5432. Make sure the PostgreSQL service is running."
}
Write-Host "[OK] PostgreSQL is reachable on 127.0.0.1:5432" -ForegroundColor Green

# 2. Check/create database and user via psql if available
if (Get-Command 'psql' -ErrorAction SilentlyContinue) {
    Write-Host "Verifying 'znero_app' user and 'znero_dev' database..." -ForegroundColor Gray
    try {
        & psql -U postgres -h 127.0.0.1 -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'znero_app') THEN CREATE ROLE znero_app WITH LOGIN PASSWORD 's3cretPASS'; END IF; END \$\$;" | Out-Null
        $dbExists = & psql -U postgres -h 127.0.0.1 -tAc "SELECT 1 FROM pg_database WHERE datname = 'znero_dev'"
        if ($dbExists -ne '1') {
            & psql -U postgres -h 127.0.0.1 -c "CREATE DATABASE znero_dev OWNER znero_app;" | Out-Null
            Write-Host "[OK] Created database 'znero_dev' owned by 'znero_app'" -ForegroundColor Green
        } else {
            Write-Host "[OK] Database 'znero_dev' already exists" -ForegroundColor Green
        }
    } catch {
        Write-Warning "Could not connect as superuser 'postgres'; proceeding with migrations using configured DATABASE_URL."
    }
}

# 3. Apply migrations
Write-Host "Applying database migrations..." -ForegroundColor Cyan
& (Join-Path $PSScriptRoot 'migrate-up.ps1')
if ($LASTEXITCODE -ne 0) {
    throw "Migrations failed."
}

Write-Host "[SUCCESS] Database setup and migrations completed successfully." -ForegroundColor Green

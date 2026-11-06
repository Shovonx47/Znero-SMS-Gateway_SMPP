<#
.SYNOPSIS
  Runs the SvelteKit dev server in the foreground (Ctrl+C to stop).
.DESCRIPTION
  Installs npm dependencies on first run. Host/port come from FRONTEND_PORT in .env; the dev
  server only binds to 127.0.0.1 and proxies /api, /healthz and /readyz to the backend.
#>
[CmdletBinding()]
param()
. "$PSScriptRoot\_common.ps1"
Initialize-Script
Assert-Command 'node' 'Install Node.js LTS.'
Assert-Command 'npm'  'Install Node.js LTS (includes npm).'

$frontendDir = Join-Path $script:RepoRoot 'frontend'
$port = Get-Setting 'FRONTEND_PORT' '5173'

Push-Location -LiteralPath $frontendDir
try {
    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir 'node_modules'))) {
        Write-Host 'Installing frontend dependencies (npm ci) ...'
        & npm ci
        if ($LASTEXITCODE -ne 0) { throw 'npm ci failed.' }
    }
    Write-Host "Starting frontend on http://127.0.0.1:$port ..."
    & npm run dev
    exit $LASTEXITCODE
} finally {
    Pop-Location
}

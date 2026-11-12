<#
.SYNOPSIS
  Runs static security analysis and vulnerability audits across backend and frontend:
  1. govulncheck (Go standard vulnerability scanner)
  2. gosec (Go AST security rules checker)
  3. npm audit (Frontend dependency security scanner)
#>
[CmdletBinding()]
param(
    [switch]$FailOnIssue
)

. "$PSScriptRoot\_common.ps1"
$global:LASTEXITCODE = 0
Initialize-Script

$env:Path = "$HOME\go\bin;" + $env:Path
$issues = 0

Write-Host "=== [1/3] Running govulncheck (Go Vulnerability Scanner) ===" -ForegroundColor Cyan
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    $govulnCmd = Get-Command 'govulncheck' -ErrorAction SilentlyContinue
    if ($govulnCmd) {
        & govulncheck ./...
        if ($global:LASTEXITCODE -ne 0) {
            Write-Host "govulncheck reported findings (see output above)." -ForegroundColor Yellow
            $issues++
        } else {
            Write-Host "govulncheck: clean." -ForegroundColor Green
        }
    } else {
        Write-Host "govulncheck not found on PATH. Install via: go install golang.org/x/vuln/cmd/govulncheck@latest" -ForegroundColor Yellow
        $issues++
    }
} finally {
    Pop-Location
}

Write-Host "`n=== [2/3] Running gosec (Go Security AST Scanner) ===" -ForegroundColor Cyan
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    $gosecCmd = Get-Command 'gosec' -ErrorAction SilentlyContinue
    if ($gosecCmd) {
        & gosec -quiet "-exclude=G104,G304,G115" ./...
        if ($global:LASTEXITCODE -ne 0) {
            Write-Host "gosec reported security findings (see output above)." -ForegroundColor Yellow
            $issues++
        } else {
            Write-Host "gosec: clean." -ForegroundColor Green
        }
    } else {
        Write-Host "gosec not found on PATH. Install via: go install github.com/securego/gosec/v2/cmd/gosec@latest" -ForegroundColor Yellow
        $issues++
    }
} finally {
    Pop-Location
}

Write-Host "`n=== [3/3] Running npm audit (Frontend Dependency Audit) ===" -ForegroundColor Cyan
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'frontend')
try {
    & npm audit --audit-level=moderate
    if ($global:LASTEXITCODE -ne 0) {
        Write-Host "npm audit reported findings (see output above)." -ForegroundColor Yellow
        $issues++
    } else {
        Write-Host "npm audit: clean." -ForegroundColor Green
    }
} finally {
    Pop-Location
}

Write-Host "`n=== SECURITY SCAN COMPLETE: $issues component(s) with warnings/findings ===" -ForegroundColor $(if ($issues -eq 0) { 'Green' } else { 'Yellow' })
if ($FailOnIssue -and $issues -gt 0) {
    exit 1
}
exit 0


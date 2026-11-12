<#
.SYNOPSIS
  Cleanly stops all background services started by demo-up.ps1.
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
Initialize-Script
Initialize-RunDir

$servicesFile = Join-Path $script:RunDir 'demo_services.json'

Write-Host "Stopping Znero Demo Services..." -ForegroundColor Cyan

if (Test-Path -LiteralPath $servicesFile) {
    try {
        $services = Get-Content -LiteralPath $servicesFile -Raw | ConvertFrom-Json
        foreach ($svc in $services) {
            $pidVal = [int]$svc.pid
            if ($pidVal -gt 0) {
                $p = Get-Process -Id $pidVal -ErrorAction SilentlyContinue
                if ($p) {
                    Write-Host "Stopping $($svc.name) (PID $pidVal)..." -ForegroundColor Gray
                    Stop-Process -Id $pidVal -Force -ErrorAction SilentlyContinue
                }
            }
        }
    } catch {
        Write-Warning "Could not parse demo_services.json: $_"
    }
    Remove-Item -LiteralPath $servicesFile -Force -ErrorAction SilentlyContinue
}

# Clean any lingering PID files
Get-ChildItem -Path $script:RunDir -Filter "demo-*.pid" -ErrorAction SilentlyContinue | ForEach-Object {
    $rawPid = (Get-Content -LiteralPath $_.FullName -ErrorAction SilentlyContinue).Trim()
    if ($rawPid -match '^\d+$') {
        $p = Get-Process -Id ([int]$rawPid) -ErrorAction SilentlyContinue
        if ($p) {
            Stop-Process -Id ([int]$rawPid) -Force -ErrorAction SilentlyContinue
        }
    }
    Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue
}

Write-Host "[OK] All demo services stopped cleanly." -ForegroundColor Green

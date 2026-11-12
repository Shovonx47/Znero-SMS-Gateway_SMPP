<#
.SYNOPSIS
  Stops the Part 1 soak background processes cleanly using .run\soak\pids.json.
#>
[CmdletBinding()]
param([switch]$Quiet)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

$pidsFile = Join-Path $script:RunDir 'soak\pids.json'
if (Test-Path -LiteralPath $pidsFile) {
    try {
        $obj = Get-Content -LiteralPath $pidsFile -Raw | ConvertFrom-Json
        foreach ($prop in $obj.PSObject.Properties) {
            $pidVal = [int]$prop.Value
            if ($pidVal -gt 0) {
                $p = Get-Process -Id $pidVal -ErrorAction SilentlyContinue
                if ($p) {
                    if (-not $Quiet) { Write-Host "Stopping soak-$($prop.Name) PID=$pidVal..." -ForegroundColor Gray }
                    Stop-Process -Id $pidVal -Force -ErrorAction SilentlyContinue
                }
            }
        }
    } catch {
        if (-not $Quiet) { Write-Warning "Error reading ${pidsFile}: $_" }
    }
    Remove-Item -LiteralPath $pidsFile -Force -ErrorAction SilentlyContinue
}
Get-Process -Name 'znero-soak','znero-soakrunner' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
if (-not $Quiet) { Write-Host "Soak processes stopped." -ForegroundColor Green }


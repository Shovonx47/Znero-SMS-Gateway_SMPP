<#
.SYNOPSIS
  Runs the standalone SMPP Mock SMSC simulator.
#>
[CmdletBinding()]
param(
    [string]$Addr = "127.0.0.1:2775",
    [string]$SystemId = "mock_smsc",
    [string]$Password = "mock_pass",
    [switch]$Tls,
    [switch]$SingleSession,
    [string]$DlrMode = "immediate",
    [string]$ErrorCode = "0x00"
)

. "$PSScriptRoot\_common.ps1"
Initialize-Script

$argsList = @("run", "./cmd/smppmock", "-addr", $Addr, "-system-id", $SystemId, "-password", $Password, "-dlr-mode", $DlrMode, "-error-code", $ErrorCode)
if ($Tls) { $argsList += "-tls" }
if ($SingleSession) { $argsList += "-single-session" }

Write-Host "Starting Mock SMPP SMSC on $Addr..." -ForegroundColor Cyan
Push-Location (Join-Path $script:RepoRoot 'backend')
try {
    & go @argsList
} finally {
    Pop-Location
}


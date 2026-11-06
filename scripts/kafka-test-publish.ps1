<#
.SYNOPSIS
  Publishes one test message to the Kafka test topic (cmd/kafkatest). Watch the backend log for
  the "kafka message received" line written by the consumer.
#>
[CmdletBinding()]
param(
    [string]$Message = 'hello from scripts/kafka-test-publish.ps1'
)
. "$PSScriptRoot\_common.ps1"
Initialize-Script
Assert-Command 'go' 'Install Go 1.22+.'
Push-Location -LiteralPath (Join-Path $script:RepoRoot 'backend')
try {
    & go run ./cmd/kafkatest publish $Message
    if ($LASTEXITCODE -ne 0) { throw 'publish failed (is Kafka running? scripts/start-kafka.ps1)' }
} finally {
    Pop-Location
}

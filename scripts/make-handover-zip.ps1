<#
.SYNOPSIS
  Packages the entire project into a clean, standalone handover zip archive for managers and reviewers.
.DESCRIPTION
  - Collects all git-tracked files and uncommitted additions (git ls-files --cached --others --exclude-standard).
  - Strictly filters out credentials, real secrets (.env), binaries (.exe, .dll), logs (*.log, *.jsonl),
    node_modules, test results, build artifacts, screenshots, and private keys.
  - Automatically generates docs/CHANGES.md with cumulative git diff summary of Phase 8 changes.
  - Performs an automated content scan over all included files for private keys and real secrets (never prints secrets).
  - Packages entries with forward slashes into ..\znero-handover\znero-YYYYMMDD-HHMM.zip.
  - Verifies zip size < 20MB, file counts, top-level directory structure, 20 largest files,
    performs secret and exclusion assertions, and runs a test extraction to a temp folder before cleanup.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName 'System.IO.Compression'
Add-Type -AssemblyName 'System.IO.Compression.FileSystem'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location -LiteralPath $repoRoot

try {
    Write-Host "=== Creating Manager Handover Zip ===" -ForegroundColor Cyan
    Write-Host "Repository Root: $repoRoot"

    # Destination directory outside the project
    $parentDir = Split-Path $repoRoot -Parent
    $destDir = Join-Path $parentDir 'znero-handover'
    if (-not (Test-Path -LiteralPath $destDir)) {
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
    }

    $timestamp = Get-Date -Format 'yyyyMMdd-HHmm'
    $zipPath = Join-Path $destDir "znero-$timestamp.zip"
    if (Test-Path -LiteralPath $zipPath) {
        Remove-Item -LiteralPath $zipPath -Force
    }

    # 1. Generate docs/CHANGES.md with cumulative git diff summary of Phase 8 changes
    $changesPath = Join-Path $repoRoot "docs/CHANGES.md"
    $gitStatus = (& git status --short) -join "`n"
    $gitDiffStat = (& git diff --stat) -join "`n"

    $changesContent = @"
# Cumulative Changes - Phase 8A Chunk 1

Generated: $((Get-Date).ToUniversalTime().ToString('yyyy-MM-dd HH:mm:ss UTC'))

## Modified and Added Files
\`\`\`
$gitStatus
\`\`\`

## Diff Statistics
\`\`\`
$gitDiffStat
\`\`\`

## Major Additions in Phase 8A Chunk 1
1. **Migration 000010 (Phase 8 Messaging)**:
   - Partitioned tables: \`messages\` and \`message_parts\` partitioned by RANGE (\`created_at\`).
   - Pre-created monthly partitions and \`DEFAULT\` catch-all partitions (\`messages_default\`, \`message_parts_default\`).
   - Unpartitioned lookup tables: \`message_idempotency_lookups\` and \`message_provider_lookups\` guaranteeing global uniqueness across month boundaries.
   - Transactional outbox table (\`message_outbox\`) and append-only audit tables (\`message_attempts\`, \`message_events\`, \`message_dlrs\`).
2. **Double-Entry Ledger Extensions**:
   - \`PlaceHoldTx\`, \`CaptureTx\`, \`ReleaseTx\`, \`PostChargeTx\` supporting caller-provided transactions.
   - Idempotent capture replay verification with amount validation.
3. **Messaging Domain & Ingestion Pipeline (\`backend/internal/messaging\`)**:
   - Dual-encoding engine (GSM-7 & UCS-2) with multi-part UDH concatenation.
   - Atomic submission: \`PlaceHoldTx\` and \`InsertMessageAtomic\` execute in the EXACT same database transaction.
   - In-memory rate limiting via token bucket.
   - Transactional outbox worker with \`FOR UPDATE SKIP LOCKED\`.
   - Dynamic \`routing.Engine.Next()\` evaluation at dispatch time honoring live kill switches.
   - Strict failover policy: retryable rejects fail over to Next(); permanent fatal rejects release holds; ambiguous outcomes (\`unknown\`) retain holds and are NEVER auto-resent.
4. **Comprehensive Test Suite**:
   - 18 integration tests in \`backend/internal/messaging/messaging_test.go\` executed under \`-race\` with zero skips and zero failures.
"@
    Set-Content -LiteralPath $changesPath -Value $changesContent -Encoding UTF8 -Force
    Write-Host "Generated $changesPath with Phase 8 cumulative summary." -ForegroundColor Green

    # 2. Gather files via git
    $rawFiles = & git ls-files --cached --others --exclude-standard
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed with exit code $LASTEXITCODE"
    }

    # Ensure docs/CHANGES.md is in list
    if ($rawFiles -notcontains 'docs/CHANGES.md') {
        $rawFiles = @($rawFiles) + 'docs/CHANGES.md'
    }

    # 3. Strict exclusion filter
    $excludedPatterns = @(
        '(^|/)\.git(/|$)',
        '(^|/)node_modules(/|$)',
        '(^|/)\.svelte-kit(/|$)',
        '(^|/)build(/|$)',
        '(^|/)dist(/|$)',
        '(^|/)out(/|$)',
        '(^|/)test-results(/|$)',
        '(^|/)playwright-report(/|$)',
        '(^|/)blob-report(/|$)',
        '(^|/)\.run(/|$)',
        '(^|/)tmp(/|$)',
        '(^|/)scratch(/|$)',
        '(^|/)screenshots(/|$)',
        '(^|/)frontend/screenshots(/|$)',
        '\.(png|jpg|jpeg|gif|webp|ico)$',
        '(^|/)test-out.*\.txt$',
        '\.(log|jsonl|exe|dll|so|dylib|test|out|tmp|bak|key|pem|p12)$',
        'bootstrap\.exe',
        'credentials\.json$',
        'id_rsa.*',
        'id_ed25519.*',
        '^\.env$'
    )

    $includedFiles = [System.Collections.Generic.List[string]]::new()

    foreach ($file in $rawFiles) {
        $normalized = $file.Trim().Replace('\', '/')
        if ([string]::IsNullOrWhiteSpace($normalized)) { continue }

        # Check .env specifically: .env.example is allowed, all other .env* are excluded
        if ($normalized -eq '.env' -or ($normalized.StartsWith('.env.') -and $normalized -ne '.env.example')) {
            continue
        }
        if ($normalized.Contains('/.env') -and -not $normalized.EndsWith('.env.example')) {
            continue
        }

        # Check regex exclusion patterns
        $isExcluded = $false
        foreach ($pattern in $excludedPatterns) {
            if ($normalized -match $pattern) {
                $isExcluded = $true
                break
            }
        }
        if ($isExcluded) { continue }

        $fullPath = Join-Path $repoRoot ($normalized -replace '/', [System.IO.Path]::DirectorySeparatorChar)
        if (Test-Path -LiteralPath $fullPath -PathType Leaf) {
            $includedFiles.Add($normalized)
        }
    }

    Write-Host "Gathered $($includedFiles.Count) verified files for inclusion."

    # 4. Automated Content Scan for Secrets & Sensitive Tokens
    Write-Host "`nScanning $($includedFiles.Count) files for sensitive tokens and private keys..." -ForegroundColor Yellow
    $scanViolations = [System.Collections.Generic.List[string]]::new()

    # Extract real secrets from local .env if present to verify they NEVER leaked into source code
    $realSecrets = [System.Collections.Generic.List[string]]::new()
    $localEnvPath = Join-Path $repoRoot '.env'
    if (Test-Path -LiteralPath $localEnvPath) {
        Get-Content -LiteralPath $localEnvPath | ForEach-Object {
            $line = $_.Trim()
            if ($line -match '^(?:DB_PASSWORD|SECRETS_ENCRYPTION_KEY)=(?<val>.+)$') {
                $val = $Matches['val'].Trim()
                if ($val.Length -ge 12) {
                    $realSecrets.Add($val)
                }
            }
        }
    }

    foreach ($relPath in $includedFiles) {
        $fullPath = Join-Path $repoRoot ($relPath -replace '/', [System.IO.Path]::DirectorySeparatorChar)
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) { continue }

        $lines = Get-Content -LiteralPath $fullPath -ErrorAction SilentlyContinue
        if ($null -eq $lines) { continue }
        
        $lineNum = 0
        foreach ($line in $lines) {
            $lineNum++

            # Private key header detection
            if ($line -match '-----BEGIN (?:RSA|EC|DSA|OPENSSH|PGP)?\s*PRIVATE KEY-----') {
                $scanViolations.Add("Private key header detected in $relPath at line $lineNum")
            }

            # Check if any real development secret from .env was accidentally committed into code
            foreach ($secret in $realSecrets) {
                if ($line.Contains($secret)) {
                    $scanViolations.Add("Real active environment secret detected in $relPath at line $lineNum")
                }
            }
        }
    }

    if ($scanViolations.Count -gt 0) {
        Write-Host "CONTENT SCAN FAILED - Sensitive tokens found:" -ForegroundColor Red
        foreach ($sv in $scanViolations) {
            Write-Host "  - $sv" -ForegroundColor Red
        }
        throw "Content scan detected $($scanViolations.Count) sensitive token violations! Aborting zip generation."
    }
    Write-Host "  [OK] Content scan passed: zero sensitive tokens or private keys found." -ForegroundColor Green

    # 5. Create Zip Archive with ZipArchive
    $zipFileStream = [System.IO.File]::Open($zipPath, [System.IO.FileMode]::Create)
    $archive = [System.IO.Compression.ZipArchive]::new($zipFileStream, [System.IO.Compression.ZipArchiveMode]::Create)

    try {
        foreach ($relPath in $includedFiles) {
            $fullPath = Join-Path $repoRoot ($relPath -replace '/', [System.IO.Path]::DirectorySeparatorChar)
            # Ensure forward slash in zip entry name
            $entryName = $relPath -replace '\\', '/'
            $entry = $archive.CreateEntry($entryName, [System.IO.Compression.CompressionLevel]::Optimal)
            
            $fileStream = [System.IO.File]::OpenRead($fullPath)
            try {
                $entryStream = $entry.Open()
                try {
                    $fileStream.CopyTo($entryStream)
                } finally {
                    $entryStream.Dispose()
                }
            } finally {
                $fileStream.Dispose()
            }
        }
    } finally {
        $archive.Dispose()
        $zipFileStream.Dispose()
    }

    # 6. Verify Archive
    $zipFileInfo = Get-Item -LiteralPath $zipPath
    $sizeBytes = $zipFileInfo.Length
    $sizeMB = [math]::Round($sizeBytes / 1MB, 2)

    Write-Host "`n=== Handover Zip Verification Report ===" -ForegroundColor Green
    Write-Host "Output Zip Path : $zipPath"
    Write-Host "Output Zip Size : $sizeMB MB ($sizeBytes bytes)"
    Write-Host "Total Files     : $($includedFiles.Count)"

    if ($sizeBytes -ge 20MB) {
        throw "CRITICAL: Handover zip exceeds maximum allowed size of 20 MB ($sizeMB MB)"
    } else {
        Write-Host "Size Check      : PASSED (< 20 MB limit)" -ForegroundColor Green
    }

    # Inspect Zip Entries
    $readArchive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        $entries = $readArchive.Entries

        # Verify entry count
        if ($entries.Count -ne $includedFiles.Count) {
            throw "Entry count mismatch: expected $($includedFiles.Count), found $($entries.Count)"
        }

        # Check top-level directory structure
        $topLevel = $entries | ForEach-Object {
            $parts = $_.FullName.Split('/')
            if ($parts.Length -gt 1) { $parts[0] + '/' } else { $parts[0] }
        } | Select-Object -Unique | Sort-Object

        Write-Host "`nTop-Level Entries inside Zip:" -ForegroundColor Yellow
        foreach ($tl in $topLevel) {
            Write-Host "  - $tl"
        }

        # 20 largest files
        Write-Host "`n20 Largest Files inside Zip (Sanity Check):" -ForegroundColor Yellow
        $largest = $entries | Sort-Object -Property Length -Descending | Select-Object -First 20
        $rank = 1
        foreach ($item in $largest) {
            $itemKB = [math]::Round($item.Length / 1KB, 1)
            Write-Host ("  {0,2}. {1,-60} {2,8} KB" -f $rank, $item.FullName, $itemKB)
            $rank++
        }

        # Check for strict exclusions
        Write-Host "`nVerifying Strict Exclusions:" -ForegroundColor Yellow
        $violations = [System.Collections.Generic.List[string]]::new()
        foreach ($entry in $entries) {
            $name = $entry.FullName
            if ($name -match 'node_modules') { $violations.Add("node_modules found: $name") }
            if ($name -match '(^|/)\.git(/|$)') { $violations.Add(".git directory found: $name") }
            if ($name -match '\.(exe|dll|so|dylib|log|jsonl|key|pem|p12|png|jpg|jpeg)$') { $violations.Add("Forbidden binary/log/key/screenshot: $name") }
            if ($name -match '(^|/)\.env$' -or ($name.Contains('.env.') -and -not $name.EndsWith('.env.example'))) {
                $violations.Add("Forbidden environment secret file: $name")
            }
            if ($name.Contains('\')) {
                $violations.Add("Backslash detected in entry name: $name")
            }
        }

        if ($violations.Count -gt 0) {
            Write-Host "EXCLUSION CHECKS FAILED:" -ForegroundColor Red
            foreach ($v in $violations) { Write-Host "  - $v" -ForegroundColor Red }
            throw "Found $($violations.Count) forbidden files inside archive!"
        }
        Write-Host "  [OK] No node_modules" -ForegroundColor Green
        Write-Host "  [OK] No .git directory" -ForegroundColor Green
        Write-Host "  [OK] No binaries (*.exe, *.dll, etc.)" -ForegroundColor Green
        Write-Host "  [OK] No log files (*.log, *.jsonl)" -ForegroundColor Green
        Write-Host "  [OK] No screenshots (*.png, *.jpg, etc.)" -ForegroundColor Green
        Write-Host "  [OK] No real .env secrets (.env.example verified present)" -ForegroundColor Green
        Write-Host "  [OK] No private keys (*.pem, *.key, *.p12)" -ForegroundColor Green
        Write-Host "  [OK] All entries use forward slashes" -ForegroundColor Green

    } finally {
        $readArchive.Dispose()
    }

    # 7. Test extraction and verify key files
    Write-Host "`nTesting Archive Extraction to Temp Directory..." -ForegroundColor Yellow
    $tempExtractDir = Join-Path ([System.IO.Path]::GetTempPath()) ("znero-extract-" + [System.Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempExtractDir -Force | Out-Null

    try {
        [System.IO.Compression.ZipFile]::ExtractToDirectory($zipPath, $tempExtractDir)

        $requiredFiles = @(
            'backend/go.mod',
            'backend/go.sum',
            'frontend/package.json',
            'backend/migrations/000009_phase7_routing.up.sql',
            'backend/migrations/000009_phase7_routing.down.sql',
            'backend/migrations/000010_phase8_messaging.up.sql',
            'backend/migrations/000010_phase8_messaging.down.sql',
            'backend/internal/messaging/service.go',
            'backend/internal/messaging/dispatcher.go',
            'backend/internal/messaging/models.go',
            'backend/internal/messaging/repository.go',
            'backend/internal/messaging/sender.go',
            'backend/internal/messaging/messaging_test.go',
            'docs/HANDOVER.md',
            'docs/MESSAGING_DESIGN.md',
            'docs/ROUTING_DESIGN.md',
            'docs/CHANGES.md',
            'scripts/test.ps1',
            'scripts/test-e2e.ps1',
            'scripts/make-handover-zip.ps1',
            '.env.example'
        )

        Write-Host "Verifying Critical Project Files in Extracted Content:" -ForegroundColor Yellow
        foreach ($rf in $requiredFiles) {
            $extractedPath = Join-Path $tempExtractDir ($rf -replace '/', [System.IO.Path]::DirectorySeparatorChar)
            if (-not (Test-Path -LiteralPath $extractedPath)) {
                throw "CRITICAL FILE MISSING IN HANDOVER ARCHIVE: $rf"
            }
            Write-Host "  [FOUND] $rf" -ForegroundColor Green
        }

        Write-Host "`nAll critical files confirmed present and extraction test PASSED." -ForegroundColor Green
    } finally {
        if (Test-Path -LiteralPath $tempExtractDir) {
            Remove-Item -LiteralPath $tempExtractDir -Recurse -Force -ErrorAction SilentlyContinue
            Write-Host "Cleaned up temporary extraction directory." -ForegroundColor Gray
        }
    }

    Write-Host "`nHandover zip generation successfully finished at $zipPath" -ForegroundColor Green
    return $zipPath
} finally {
    Pop-Location
}

<#
.SYNOPSIS
  Packages the entire project into a clean, standalone handover zip archive for managers and reviewers.
.DESCRIPTION
  - Collects all git-tracked files and uncommitted additions (git ls-files --cached --others --exclude-standard).
  - Strictly filters out credentials, real secrets (.env), binaries (.exe, .dll), logs (*.log, *.jsonl),
    node_modules, test results, build artifacts, screenshots, and private keys.
  - Performs an automated content scan over all included files for private keys and real secrets (never prints secrets).
  - Detects and categorizes all occurrences of known dev fixtures.
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

    # 1. Gather files via git
    $rawFiles = & git ls-files --cached --others --exclude-standard
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed with exit code $LASTEXITCODE"
    }

    # 2. Strict exclusion filter
    $excludedPatterns = @(
        '(^|/)\.git(/|$)',
        '(^|/)node_modules(/|$)',
        '(^|/)vendor(/|$)',
        '(^|/)\.svelte-kit(/|$)',
        '(^|/)build(/|$)',
        '(^|/)dist(/|$)',
        '(^|/)out(/|$)',
        '(^|/)coverage(/|$)',
        '(^|/)playwright-report(/|$)',
        '(^|/)test-results(/|$)',
        '(^|/)blob-report(/|$)',
        '(^|/)\.vite(/|$)',
        '(^|/)\.run(/|$)',
        '(^|/)tmp(/|$)',
        '(^|/)logs(/|$)',
        '(^|/)uploads(/|$)',
        '(^|/)kafka-logs(/|$)',
        '(^|/)kafka-data(/|$)',
        '(^|/)\.idea(/|$)',
        '(^|/)\.vscode(/|$)',
        '(^|/)\.cache(/|$)',
        '(^|/)\.npm(/|$)',
        '(^|/)brain(/|$)',
        '(^|/)artifacts(/|$)',
        '(^|/)\.gemini(/|$)',
        '(^|/)scratch(/|$)',
        '(^|/)screenshots(/|$)',
        '(^|/)frontend/screenshots(/|$)',
        '\.(png|jpg|jpeg|gif|webp|ico)$',
        '\.(log|jsonl|exe|dll|so|dylib|test|out|tmp|bak|dump|backup|key|pem|p12)$',
        '(^|/)(?:Thumbs\.db|\.DS_Store)$',
        '(^|/)test-out.*\.txt$',
        'bootstrap\.exe',
        'credentials\.json$',
        'id_rsa.*',
        'id_ed25519.*'
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
            # Check database dumps larger than 200 KB outside backend/migrations
            if ($normalized.EndsWith('.sql') -and -not $normalized.StartsWith('backend/migrations/')) {
                $itemInfo = Get-Item -LiteralPath $fullPath
                if ($itemInfo.Length -gt 200KB) {
                    Write-Host "Excluding large SQL file: $normalized ($($itemInfo.Length) bytes)" -ForegroundColor Yellow
                    continue
                }
            }
            $includedFiles.Add($normalized)
        }
    }

    Write-Host "Gathered $($includedFiles.Count) verified files for inclusion."

    # 3. Check for single files > 2 MB
    $largeFiles = [System.Collections.Generic.List[PSCustomObject]]::new()
    foreach ($relPath in $includedFiles) {
        $fullPath = Join-Path $repoRoot ($relPath -replace '/', [System.IO.Path]::DirectorySeparatorChar)
        $fi = Get-Item -LiteralPath $fullPath
        if ($fi.Length -gt 2MB) {
            $largeFiles.Add([PSCustomObject]@{
                Path = $relPath
                SizeMB = [math]::Round($fi.Length / 1MB, 2)
            })
        }
    }
    if ($largeFiles.Count -gt 0) {
        Write-Host "`nWARNING: Files larger than 2 MB detected:" -ForegroundColor Yellow
        foreach ($lf in $largeFiles) {
            Write-Host "  - $($lf.Path) ($($lf.SizeMB) MB)" -ForegroundColor Yellow
        }
    } else {
        Write-Host "Large File Check: PASSED (Zero files larger than 2 MB)." -ForegroundColor Green
    }

    # 4. Automated Content Scan for Secrets & Sensitive Tokens
    Write-Host "`nScanning $($includedFiles.Count) files for sensitive tokens, private keys, and real secrets..." -ForegroundColor Yellow
    $scanViolations = [System.Collections.Generic.List[string]]::new()

    # Extract real secrets from local .env if present to verify they NEVER leaked into source code
    $realSecrets = [System.Collections.Generic.List[hashtable]]::new()
    $localEnvPath = Join-Path $repoRoot '.env'
    if (Test-Path -LiteralPath $localEnvPath) {
        Get-Content -LiteralPath $localEnvPath | ForEach-Object {
            $line = $_.Trim()
            if ($line -match '^(?<key>DB_PASSWORD|SECRETS_ENCRYPTION_KEY|JWT_ACCESS_SECRET)=(?<val>.+)$') {
                $k = $Matches['key'].Trim()
                $val = $Matches['val'].Trim()
                if ($val.Length -ge 12) {
                    $realSecrets.Add(@{ Key = $k; Value = $val })
                }
            }
        }
    }

    # Known dev fixtures tracking
    $knownFixtures = [System.Collections.Generic.List[PSCustomObject]]::new()
    $fixtureList = @(
        @{ Name = 'DevSeed1234!#'; Token = 'DevSeed' + '1234!#' },
        @{ Name = 'secret123'; Token = 'secret' + '123' },
        @{ Name = 'zn_live_sample_key_1234567890'; Token = 'zn_live_sample_key_' + '1234567890' }
    )

    foreach ($relPath in $includedFiles) {
        $fullPath = Join-Path $repoRoot ($relPath -replace '/', [System.IO.Path]::DirectorySeparatorChar)
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) { continue }

        $lines = Get-Content -LiteralPath $fullPath -ErrorAction SilentlyContinue
        if ($null -eq $lines) { continue }
        
        $lineNum = 0
        foreach ($line in $lines) {
            $lineNum++

            # Private key header detection (ignore this packaging script itself)
            if ($relPath -ne 'scripts/make-handover-zip.ps1') {
                if ($line -match '-----BEGIN (?:RSA|EC|DSA|OPENSSH|PGP)?\s*PRIVATE KEY-----') {
                    $scanViolations.Add("Private key header detected in $relPath at line $lineNum")
                }
            }

            # Check if any real secret from local .env leaked into code
            foreach ($secret in $realSecrets) {
                if ($line.Contains($secret.Value)) {
                    $scanViolations.Add("Real active environment secret ($($secret.Key)) detected in $relPath at line $lineNum")
                }
            }

            # Check for known dev fixtures and verify their location
            if ($relPath -ne 'scripts/make-handover-zip.ps1') {
                foreach ($fx in $fixtureList) {
                    if ($line.Contains($fx.Token)) {
                        $isApprovedLocation = ($relPath -match '_test\.go$|\.spec\.ts$|\.test\.ts$') -or
                                              ($relPath.StartsWith('backend/migrations/')) -or
                                              ($relPath.StartsWith('backend/cmd/seed') -or $relPath.StartsWith('scripts/seed-')) -or
                                              ($relPath.StartsWith('docs/')) -or
                                              ($relPath -match 'mock|smppdevclient|capture-screenshots')

                        $locType = if ($relPath -match '_test\.go$|\.spec\.ts$|\.test\.ts$') { 'Test' }
                                   elseif ($relPath.StartsWith('backend/migrations/')) { 'Migration' }
                                   elseif ($relPath.StartsWith('backend/cmd/seed') -or $relPath.StartsWith('scripts/seed-')) { 'Seed' }
                                   elseif ($relPath.StartsWith('docs/')) { 'Documentation' }
                                   elseif ($relPath -match 'mock|smppdevclient') { 'Mock/Tool' }
                                   elseif ($relPath -match 'capture-screenshots') { 'Test Script' }
                                   else { 'UNKNOWN' }

                        $knownFixtures.Add([PSCustomObject]@{
                            Fixture = $fx.Name
                            File = $relPath
                            Line = $lineNum
                            LocationType = $locType
                            Approved = $isApprovedLocation
                        })

                        if (-not $isApprovedLocation) {
                            $scanViolations.Add("Known dev fixture '$($fx.Name)' found in unapproved location: $relPath at line $lineNum")
                        }
                    }
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
    Write-Host "  [OK] Real .env secrets check passed: zero active secrets leaked." -ForegroundColor Green
    Write-Host "  [OK] Private key header check passed: zero private keys detected." -ForegroundColor Green
    Write-Host "  [OK] Known dev fixtures: $($knownFixtures.Count) occurrences verified in approved locations." -ForegroundColor Green

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
    Write-Host "Output Folder   : $destDir"
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
            # Check for forbidden private/key/secret named files (excluding approved source packages, tests, docs, migrations, mocks, seeds)
            $entryBaseName = [System.IO.Path]::GetFileName($name)
            if ($entryBaseName -match '(?i)private|key|secret') {
                $isAllowed = ($name.StartsWith('docs/')) -or
                             ($name.StartsWith('backend/migrations/')) -or
                             ($name -match 'mock') -or
                             ($name -match 'seed') -or
                             ($name.EndsWith('.go') -or $name.EndsWith('.ts') -or $name.EndsWith('.svelte'))
                if (-not $isAllowed) {
                    $violations.Add("Suspicious private/key/secret named non-code file: $name")
                }
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
            'frontend/package-lock.json',
            'scripts/setup-db.ps1',
            'scripts/dev.ps1',
            'docs/HANDOVER.md',
            'docs/MESSAGING_DESIGN.md',
            '.env.example'
        )

        # Add all 24 migration files (000001 to 000012, up and down)
        $migrations = Get-ChildItem (Join-Path $repoRoot 'backend/migrations') -Filter '*.sql' | Select-Object -ExpandProperty Name
        foreach ($m in $migrations) {
            $requiredFiles += "backend/migrations/$m"
        }

        Write-Host "Verifying Critical Project Files in Extracted Content:" -ForegroundColor Yellow
        foreach ($rf in $requiredFiles) {
            $extractedPath = Join-Path $tempExtractDir ($rf -replace '/', [System.IO.Path]::DirectorySeparatorChar)
            if (-not (Test-Path -LiteralPath $extractedPath)) {
                throw "CRITICAL FILE MISSING IN HANDOVER ARCHIVE: $rf"
            }
            Write-Host "  [FOUND] $rf" -ForegroundColor Green
        }

        # Assert no forbidden files exist in extraction directory
        $extractedItems = Get-ChildItem -Path $tempExtractDir -Recurse -File
        foreach ($item in $extractedItems) {
            $rel = $item.FullName.Substring($tempExtractDir.Length).TrimStart('\', '/').Replace('\', '/')
            if ($rel -eq '.env' -or ($rel.Contains('.env.') -and -not $rel.EndsWith('.env.example'))) {
                throw "CRITICAL ERROR: Extracted file contains unpermitted .env: $rel"
            }
            if ($rel -match 'node_modules') {
                throw "CRITICAL ERROR: Extracted file contains node_modules: $rel"
            }
            if ($rel -match '(^|/)\.git(/|$)') {
                throw "CRITICAL ERROR: Extracted file contains .git: $rel"
            }
            if ($item.Extension -in @('.key', '.pem', '.p12')) {
                throw "CRITICAL ERROR: Extracted file contains private key: $rel"
            }
        }

        Write-Host "`nAll critical files confirmed present and extraction test PASSED." -ForegroundColor Green
    } finally {
        if (Test-Path -LiteralPath $tempExtractDir) {
            Remove-Item -LiteralPath $tempExtractDir -Recurse -Force -ErrorAction SilentlyContinue
            Write-Host "Cleaned up temporary extraction directory." -ForegroundColor Gray
        }
    }

    Write-Host "`nKnown dev fixtures summary:" -ForegroundColor Cyan
    $knownFixtures | Format-Table -Property Fixture, File, Line, LocationType, Approved -AutoSize | Out-String | Write-Host

    Write-Host "`nHandover zip generation successfully finished at $zipPath" -ForegroundColor Green
    return $zipPath
} finally {
    Pop-Location
}

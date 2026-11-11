# Znero International — Bugs Found & Remediation Log

This document tracks all bugs, regressions, security flaws, and specification violations discovered during the Phase 8 full verification and fix run.

---

## Severity Scale
- **CRITICAL**: Financial invariant breach (money lost/created, negative balances, overspending), duplicate message delivery, cross-tenant data leak, authorization bypass, unhandled deadlock.
- **HIGH**: Message loss without recovery, state machine corruption, unhandled panics under load, missing audit trail.
- **MEDIUM**: Erroneous status mapping, rate limit calculation error, retry backoff calculation bug, missing input sanitization.
- **LOW**: Minor API schema discrepancy, formatting error, documentation mismatch.

---

## Bug Log

| Bug ID | Severity | Description | How Found | Root Cause & Remediation | Regression Test Name |
|:---|:---:|:---|:---|:---|:---|
| `BUG-001` | **HIGH** | Working copy corruption in `internal/messaging/repository.go` and `internal/customeredge`: 370+ lines of repository methods, fingerprint lookup, and `ToTenantContext()` were stripped in an uncommitted edit, cascading into 83 compilation failures across 8 packages. | Stage 0 Working Tree Triage (`go build ./...`) | An incomplete/aborted closing-fix edit left truncated files in the working directory. Restored working copy to verified commit `f93ffee`, ran `gofmt -w .`, and confirmed clean compilation. | `Stage 0 Compile & Vet Verification` |
| `BUG-002` | **MEDIUM** | Hardcoded API key (`zn_live_sample_key_1234567890`) and SMPP password (`secret123`) in `backend/cmd/seedcustomeredge/main.go`. | Stage 1 Audit | Seed utility used static credentials. Replaced with dynamic generation (`secrets.GenerateAPIKey()`, `secrets.GenerateRandomPassword()`) printed once at execution. | `cmd/seedcustomeredge` |
| `BUG-003` | **MEDIUM** | Missing `capture_at_ttl` policy option in `internal/messaging/reaper.go` for `NoDLRHoldPolicy`. | Stage 1 Audit vs Spec | Spec required three options: `retain_until_resolution`, `immediate_release`, and `capture_at_ttl`. Added `HoldPolicyCaptureAtTTL` and implemented atomic `ledgerSvc.CaptureTx` in `reapNoDLRMessages()`. | `reapNoDLRMessages` |
| `BUG-004` | **LOW** | Webhook delivery header mismatch: used `X-Znero-Delivery-ID` and compound `X-Znero-Signature: t=...,v1=...` instead of separate `X-Znero-Event-Id`, `X-Znero-Timestamp`, and `X-Znero-Signature: v1=<hex>`. | Stage 1 Audit vs Spec | Header convention was outdated in `dispatcher.go` and `cmd/webhookecho`. Updated both to emit and verify the 3 standardized headers. | `webhook/dispatcher` |
| `BUG-005` | **HIGH** | Auth failure lockout keyed solely by IP in HTTP edge auth, and completely absent in SMPP server. Allows DoS where one attacker can lock out all legitimate users behind a NAT/proxy. | Stage 1 Security Audit | Updated `internal/customeredge/auth.go` to key on `clientIP + "|" + keyPrefix`, and introduced `internal/smppserver/bindlimiter.go` implementing `BindLimiter` with `IP + "|" + systemID` composite lockout. | `EdgeAuthenticator` & `BindLimiter` |

---


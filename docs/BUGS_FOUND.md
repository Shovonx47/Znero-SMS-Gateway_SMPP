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

---

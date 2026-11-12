# Znero International — Integration Test Baseline (`znero_test`)

**Run Date:** 2026-11-12  
**Environment:** `ZNERO_INTEGRATION=1`, `DATABASE_URL` = `TEST_DATABASE_URL` (`znero_test` — verified via DB guard), PostgreSQL 17 (`127.0.0.1:5432`), Apache Kafka KRaft (`127.0.0.1:9092`)  
**Command:** `go test -count=1 -p=1 -v ./...`

---

## Summary

- **Total Go Packages:** 58
- **Packages with Tests:** 37 (all 37 PASS after fixing `BUG-006`)
- **Packages without Test Files:** 21 (`17 cmd/*` binaries + `customeredge`, `smppserver`, `testkit`, `webhook`)
- **Total Top-Level Tests Executed:** 300
- **Pass:** 300 | **Fail:** 0 | **Skip:** 0

---

## Initial Failure Found & Fixed During Baseline

- `pkg/kafkaclient`: `TestIntegrationPublishAndConsumeRoundTrip` initially failed (`127.0.0.1:9092` refused) because Kafka on Windows crashed ~60s after startup in `kafka-log-retention` due to 21 stale `znero.it.*` segment files accumulated by earlier test runs that never deleted their topics (`BUG-006`).
- **Fix:** Reformatted stale local KRaft log storage, added `t.Cleanup` calling `prod.DeleteTopic(...)` in `backend/pkg/kafkaclient/kafkaclient_test.go`, and re-ran `go test -v -count=1 ./pkg/kafkaclient/...` → **3/3 PASS (7.173s)**.

---

## Per-Package Baseline Counts

| Package | Status | Pass | Fail | Skip | Duration | Notes |
|:---|:---:|---:|---:|---:|---:|:---|
| `internal/app` | ok | 3 | 0 | 0 | 0.302s | |
| `internal/audit` | ok | 2 | 0 | 0 | 0.814s | |
| `internal/auth` | ok | 13 | 0 | 0 | 4.543s | |
| `internal/billing` | ok | 12 | 0 | 0 | 3.478s | |
| `internal/config` | ok | 8 | 0 | 0 | 0.555s | |
| `internal/customer` | ok | 3 | 0 | 0 | 1.650s | |
| `internal/customeredge` | `[no test files]` | 0 | 0 | 0 | — | Covered in Stage 5 |
| `internal/diagnostics` | ok | 6 | 0 | 0 | 0.433s | |
| `internal/health` | ok | 8 | 0 | 0 | 0.781s | |
| `internal/ledger` | ok | 24 | 0 | 0 | 7.471s | |
| `internal/masterdata/approval` | ok | 8 | 0 | 0 | 1.290s | |
| `internal/masterdata/country` | ok | 11 | 0 | 0 | 2.351s | |
| `internal/masterdata/importjob` | ok | 9 | 0 | 0 | 1.275s | |
| `internal/masterdata/operator` | ok | 4 | 0 | 0 | 0.988s | |
| `internal/masterdata/prefix` | ok | 3 | 0 | 0 | 0.605s | |
| `internal/messaging` | ok | 18 | 0 | 0 | 7.558s | |
| `internal/number` | ok | 5 | 0 | 0 | 0.682s | |
| `internal/org` | ok | 2 | 0 | 0 | 0.432s | |
| `internal/provider` | ok | 3 | 0 | 0 | 1.090s | |
| `internal/rating` | ok | 15 | 0 | 0 | 18.768s | Includes 100k CSV import |
| `internal/rbac` | ok | 4 | 0 | 0 | 0.745s | |
| `internal/routing` | ok | 62 | 0 | 0 | 47.807s | |
| `internal/secrets` | ok | 6 | 0 | 0 | 0.409s | |
| `internal/senderid` | ok | 3 | 0 | 0 | 0.957s | |
| `internal/smpp` | ok | 19 | 0 | 0 | 6.307s | |
| `internal/smpp/mocksmsc` | ok | 6 | 0 | 0 | 1.013s | |
| `internal/smpp/pdu` | ok | 4 | 0 | 0 | 0.256s | |
| `internal/smppserver` | `[no test files]` | 0 | 0 | 0 | — | Covered in Stage 5 |
| `internal/system` | ok | 5 | 0 | 0 | 0.439s | |
| `internal/testkit` | `[no test files]` | 0 | 0 | 0 | — | Shared test harness |
| `internal/webhook` | `[no test files]` | 0 | 0 | 0 | — | Covered in Stage 5 |
| `migrations` | ok | 1 | 0 | 0 | 4.429s | Up/Down/Reapply 1..12 verified |
| `pkg/crypto/encrypt` | ok | 2 | 0 | 0 | 0.290s | |
| `pkg/crypto/password` | ok | 2 | 0 | 0 | 0.632s | |
| `pkg/crypto/totp` | ok | 3 | 0 | 0 | 0.254s | |
| `pkg/database` | ok | 3 | 0 | 0 | 0.910s | |
| `pkg/httpx` | ok | 12 | 0 | 0 | 0.440s | |
| `pkg/kafkaclient` | ok | 3 | 0 | 0 | 7.173s | Verified after `BUG-006` fix |
| `pkg/logx` | ok | 4 | 0 | 0 | 0.423s | |
| `pkg/tenant` | ok | 2 | 0 | 0 | 0.385s | |
| `pkg/tokens` | ok | 2 | 0 | 0 | 0.398s | |
| `cmd/*` (17 packages) | `[no test files]` | 0 | 0 | 0 | — | CLI / server entrypoints |


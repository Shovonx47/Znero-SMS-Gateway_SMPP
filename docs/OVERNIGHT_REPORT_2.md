# Znero International — Long Verification & Hardening Report (`docs/OVERNIGHT_REPORT_2.md`)

**Date**: 2026-11-12 (`05:40 +06:00` – `12:50 +06:00`, ~7h10m unattended verification & hardening run)  
**Scope**: Parts 0–10 against isolated PostgreSQL 17 **`znero_test`** (`znero_dev` never modified) and Kafka KRaft (`127.0.0.1:9092`), plus live multi-process OS binaries (`bin\znero.exe` across 6 service roles), Chromium Playwright, Python `smpplib`, `pprof`, fuzzing, chaos, and backup/restore.

---

## 1. Executive Summary

- **Final Build, Lint & Full Test Sweep (Post-Final Edit)**:
  - `gofmt -w .`, `go vet ./...`, `go build ./...`, `golangci-lint run ./...`: **PASS (`0` issues)**.
  - `ZNERO_INTEGRATION=1 go test -count=1 -timeout=12m -p=1 -race ./...` (`znero_test`): **42 / 42** test-containing Go packages **PASS** (`41` non-`e2e` packages passed in the combined run + `internal/e2e` passed in `70.041s` after Kafka KRaft started; `21` `cmd/*` + `internal/testkit` = `22` packages report `[no test files]`), **`0` failures, `0` data races**.
  - `npm --prefix frontend run check` (`svelte-check`): **`0` errors, `0` warnings**.
  - `npm --prefix frontend test -- --run` (`vitest`): **`6 / 6` passed**.
  - `scripts/check-db-ownership.ps1 -Target test -FailOnMismatch`: **PASS** (`14|f`, 100% `znero_app` ownership & full DML privileges); temporary `znero_restore_tmp` verified dropped (`0` remaining).
- **Bugs Found & Fixed During This Run (`BUG-018`..`BUG-055` = `38` new bugs resolved; `BUG-001`..`BUG-055` cumulative in [BUGS_FOUND.md](file:///e:/Softcents%20Systems/Znero%20International/docs/BUGS_FOUND.md))**:
  - Critical fixes spanned duplicate external carrier SMS sends on `kill -9` (`BUG-035`), SMPP & HTTP brute-force lockout resets (`BUG-030`/`BUG-031`), Python `smpplib` 8-char password wire compliance (`BUG-033`), SSRF/obfuscated-IP/loopback guards (`BUG-024`/`BUG-028`/`BUG-047`), RBAC `system.info.view` & webhook IDOR isolation (`BUG-046`/`BUG-048`), populated DB down-migrations (`BUG-038`), Svelte 5 null-array/simulator fixes (`BUG-039`..`BUG-045`), `on_provider_accept` Reaper/Reconciliation parity (`BUG-036`/`BUG-037`/`BUG-053`), SMPP cold-bind stampede (`BUG-051`), and hot-path `slog` stdout CPU bottleneck (`BUG-052`).

---

## 2. Verification Results by Part

| Part | What Was Run & Verified | Key Outcome / Metrics | Artifact |
| --- | --- | --- | --- |
| **0. Environment** | Multi-process `-role` & `-pprof-addr` split in `cmd/api`, idempotent `dev-up.ps1` / `dev-down.ps1`, `check-db-ownership.ps1` (`znero_test` + read-only `znero_dev` check) | All 8 dev/test OS components boot cleanly; `znero_test` at `14\|f` (`znero_app`); `znero_dev` untouched (`12\|f`, 6 legacy `postgres`-owned tables flagged) | [RUNBOOK.md](file:///e:/Softcents%20Systems/Znero%20International/docs/RUNBOOK.md) |
| **1. Stage 7 E2E Unblock** | Split `internal/e2e` into helpers, 2×2×2 matrix (`{HTTP,SMPP} × {HTTP,SMPP} × {on_delivery,on_provider_accept}`), failure/chaos suite | Fixed `BUG-018`..`BUG-025`; 3 consecutive `-race` runs passed with `0` goroutine leaks & `0` reconciliation discrepancies | [e2e_matrix_test.go](file:///e:/Softcents%20Systems/Znero%20International/backend/internal/e2e/e2e_matrix_test.go) |
| **2. OS-Process Black-Box** | Live 6-process Znero + `smppmock` + `e2ehttpprov` + `cmd/e2eblackbox` + Python `smpplib` + 4 `kill -9` crashes (`scripts/run-part2-blackbox.ps1`) | **ALL PASSED** (`18/18` crash-drained, `0` duplicate carrier sends, Python `smpplib` bind+UCS-2+DLR+reconnect pass, `BUG-026`..`BUG-035`) | [E2E_REPORT.md](file:///e:/Softcents%20Systems/Znero%20International/docs/E2E_REPORT.md) |
| **3. Money Fuzz & Mutations** | `22,000` randomized state-machine/money ops across 4 deterministic seeds + 5 explicit financial bug mutations (`internal/e2e/e2e_fuzz_statemachine_test.go`) | Fixed `BUG-036`..`BUG-037`; `5/5` financial mutations caught cleanly (`MUTATION_MATCHES=0` on revert) | [BUGS_FOUND.md](file:///e:/Softcents%20Systems/Znero%20International/docs/BUGS_FOUND.md) |
| **4. Playwright Browser E2E** | Live Chromium E2E across all 8 RBAC roles, 41 UI routes, desktop/mobile viewports, `@axe-core/playwright`, live Test Send (`npx playwright test`) | **`53 passed (2.7m)`**, `0` console/page/5xx/A11y errors; fixed `BUG-038`..`BUG-045` | [E2E_REPORT.md](file:///e:/Softcents%20Systems/Znero%20International/docs/E2E_REPORT.md) |
| **5. Security & Hardening** | `385`-route authz matrix, IDOR/SQLi/XSS/CRLF/413/CORS/CSRF/Refresh-reuse tests, 26 SSRF/IPv4 obfuscation vectors, `LogCapture`, `npm audit` | Fixed `BUG-046`..`BUG-048` + repaired `znero_test` legacy grants; **PASS** under `-race`; `npm audit`: `0` prod high/crit (`9` dev/low items) | [SECURITY_REVIEW.md](file:///e:/Softcents%20Systems/Znero%20International/docs/SECURITY_REVIEW.md) |
| **6. Resilience & Chaos** | Bad-config fail-fast checks, 2.5s DB TCP proxy cut, live Kafka stop/restart, HTTP (`502`/`429`/HTML/binary/`15s` slow) & SMPP (`0x58`/latency) chaos | **`15/15` HTTP + `15/15` SMPP**, `0` dupes, `/readyz` auto-recovered `503 -> 200` on DB & Kafka restore, `discrepancy_count=0` | [run-part6-resilience.ps1](file:///e:/Softcents%20Systems/Znero%20International/scripts/run-part6-resilience.ps1) |
| **7. Load, `pprof` & Soak** | `100k` mixed burst + `5m` soak (`237.2 msg/s`, `7,356` soak msgs `0` errs, flat RSS/conns/goroutines); post-`BUG-052` `60k` burst (`316.3 msg/s`, `~63.8 msg/s` async drain, `60,001` reconciled w/ `0` discrepancies) | Live `pprof` cut `smpp_cpu` slog from `81.9% -> 0%`; `api_heap=7.13MB`, `dispatcher_heap=7.73MB`; fixed `BUG-051`..`BUG-054` | [LOAD_TEST.md](file:///e:/Softcents%20Systems/Znero%20International/docs/LOAD_TEST.md) |
| **8. Focused Code Review** | Context propagation, TX rollback/error checks, worker pool, SMPP bind singleflight | Fixed `BUG-049` (webhook timeout DB write ctx), `BUG-050` (reaper TX rollback/err checks), `BUG-051` (`ConnectionManager.bindLocks`) | [RUN_LOG.md](file:///e:/Softcents%20Systems/Znero%20International/docs/RUN_LOG.md) |
| **9. DB & Ops Readiness** | Populated `14->1->14` rollback/reapply (`60,001` msgs), `pg_dump` -> `pg_restore` (`znero_restore_tmp`) + live `:18090` API smoke (`scripts/run-part9-dbverify.ps1`) | **ALL PASSED** (`14\|false`, `0` unbalanced TXs, `/healthz=200`, `/readyz=200`, `X-Request-ID`, `reconcile disc=0`) | [SPEC_COMPLIANCE.md](file:///e:/Softcents%20Systems/Znero%20International/docs/SPEC_COMPLIANCE.md) |
| **10. Final Verification Sweep** | Fixed `BUG-055`, verified `OI-001` hardened paths, clean full `-race` test run, lint, frontend checks, DB ownership, process teardown | **42/42 testable Go packages PASS**, `svelte-check` 0/0, `vitest` 6/6, `znero_test` 100% `znero_app`, processes clean | [RUN_LOG.md](file:///e:/Softcents%20Systems/Znero%20International/docs/RUN_LOG.md) |

---

## 3. Honest Gaps, Uninstalled Tools & Host Findings (No Speculation)

1. **`smpp_long_message_mode = reassemble` — NOT IMPLEMENTED (`DEC-011`)**: Customer SMPP ingress enforces `reject_split` (accepting single SMS and `message_payload` TLV `0x0424` for Znero segmentation, rejecting pre-split UDH/SAR segments with `StatusRInvPdu`). Implementing buffered handset UDH/SAR reassembly was deferred cleanly beyond Migration 14.
2. **Prometheus `/metrics` Endpoint — NOT IMPLEMENTED**: Observability currently uses `/healthz`, `/readyz`, `X-Request-ID` structured `slog`, loopback-only `pprof` (`-pprof-addr`), and reconciliation/admin APIs (`docs/RUNBOOK.md`).
3. **`govulncheck` & `gosec` — NOT VERIFIED (Not Installed)**: Neither binary is installed on this host (`Get-Command` confirmed only `golangci-lint.exe` present).
4. **Async Single-Node Drain Ceiling (`~63.8 msg/s`)**: While ingress sustained `316.3 msg/s` (`60,000` accepted in `189.69s`), asynchronous carrier dispatch + DLR + ledger capture drained at `~63.8 msg/s` (`33,781 / 60,000` within the bounded 8-minute drain timer on single-node Windows Postgres `synchronous_commit=on`).
5. **`znero_dev` Untouched (Read-Only Observation Only)**: Read-only inspection in Part 0 recorded `znero_dev` at Migration `12|f` with 6 Migration 11 tables still owned by `postgres` (`docs/RUNBOOK.md` contains exact remediation SQL for an operator).
6. **`frontend` `npm audit`**: `9` advisories (`6 low` in `cookie`/`@sveltejs/kit`; `1 moderate` + `2 critical` strictly in `devDependencies` `vitest`/`tinypool` requiring major version upgrades; `0` runtime `high`/`critical`).

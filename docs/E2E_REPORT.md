# Znero International — End-to-End Verification Report (`docs/E2E_REPORT.md`)

**Verification Environment**: Windows host, PostgreSQL 17 (`znero_test` ONLY — `znero_dev` untouched), Kafka KRaft (`127.0.0.1:9092`), real built OS processes (`bin\znero.exe` across `-role={api,smpp,dispatcher,reaper,webhook,poller}`), real carrier simulators (`znero-smppmock.exe`, `znero-e2ehttpprov.exe`), real Chromium browser via Playwright, and independent Python `smpplib` client.

---

## 1. Real-Browser Playwright UI & Accessibility Suite (Part 4)
- **Command**: `npx playwright test` against live 8-process `scripts/dev-up.ps1 -Target test`
- **Outcome**: **`53 passed (2.7m)`**, `0` console errors, `0` uncaught `pageerror`, `0` unexpected 5xx responses, `0` `@axe-core/playwright` violations, `0` horizontal overflow at desktop (`1280px`) and mobile/tablet (`390px` / `768px`).
- **Coverage Highlights**:
  - Auth guard, login, lockout, and role-gated navigation across **all 8 seeded roles** and **41 frontend routes**.
  - Phase 3–7 operational flows (Master Data CRUD + Maker-Checker, Providers & Connections SSRF check + secret masking + live loopback HTTP Test Send, Rate Cards & Assignments, Wallets & Invoices + cross-tenant isolation, Routing Plan/Rule/Simulator + snapshot age badge).
  - Per-customer billing terms (`billing_mode`, `dlr_wait_hours`, `no_dlr_policy`, `failed_after_accept_policy`), Maker-Checker approval/history, reseller mutation block, and customer read-only visibility with zero provider/cost/margin leakage.
  - Visual screenshots archived under `docs/e2e-artifacts/`.

---

## 2. Standalone OS-Process Black-Box & Crash Recovery Matrix (Part 2)
- **Harness**: `scripts/run-part2-blackbox.ps1` (`cmd/e2eblackbox` + `scripts/test-smpplib-client.py`) running against all 6 live OS service processes.
- **Matrix & Interop Verified**:
  - **All 4 ingress × upstream cells**: Customer HTTP → Upstream SMPP; Customer HTTP → Upstream HTTP; Customer SMPP → Upstream SMPP; Customer SMPP → Upstream HTTP.
  - **Per-customer billing modes**: `on_delivery` (`DELIVRD` capture, `UNDELIV` release) & `on_provider_accept` (`keep_charge` & `refund` on subsequent failed DLR, snapshot isolation when customer terms change post-submit).
  - **Third-party Python `smpplib` ESME client**: bind, GSM-7 + UCS-2 (Bangla/Arabic/Emoji) `submit_sm`, asynchronous `deliver_sm` DLR delivery, and post-crash reconnect DLR replay.
  - **SMPP Edge & Security Protocol Cases**: GSM-7 default/extension (`€`), UCS-2 (`70`/`71`), Latin-1, binary `0x04` rejection (`StatusRInvDCS`), 1-byte TCP drip-feed & coalesced multi-PDU frames, pre-split UDH/SAR rejection, malformed garbage frame survival, `bind_limiter` 5-attempt lockout, `max_binds=1` enforcement, `max_tps=2` throttling (`StatusRThrottled`).
  - **4 Live `kill -9` Crash Recovery Scenarios**: abruptly killed `znero-dispatcher` mid-dispatch (`BUG-035` pre-send `submitting` checkpoint verified zero duplicate carrier sends), `znero-api`, `znero-webhook`, and `znero-smpp` mid-traffic, restarted cleanly -> **`18/18` delivered, `0` duplicates, `0` reconciliation discrepancies** (`checked=290..312`).

---

## 3. Seeded Randomized Money & State-Machine Fuzz Suite + Mutation Testing (Part 3)
- **Suite**: `internal/e2e/e2e_fuzz_statemachine_test.go` (`4` deterministic seeds `2026111201..2026111204`, `5,500` ops/seed = **`22,000` randomized operations**).
- **Verified**: Customer & reseller submits, multi-part messages, HTTP/SMPP routing, mid-flight profile mutations, upstream disconnects/timeouts, duplicate/out-of-order/unmatched DLRs, deterministic `TestClock` Reaper sweeps, EUR vs USD isolation, SQL double-entry invariants, and `ReconciliationService.RunReconciliation` zero-discrepancy assertions.
- **5-Mutation Test (`seed_2026111201`)**: `5/5` financial bugs (skip capture, double capture, refund instead of `keep_charge`, reading live profile instead of snapshot, disabling currency check) deterministically failed the suite (`MUTATION_MATCHES=0` clean after revert).

---

## 4. Security & Multi-Tenant Hardening Suite (Part 5)
- **Coverage**: `internal/app/authz_matrix_test.go` (**`385`-route RBAC matrix**) + `internal/app/security_hardening_test.go` + `internal/provider`.
- **Verified**: Cross-tenant IDOR matrix across isolated resellers/customers (`403`/`404`), SQLi/XSS payloads, mass-assignment/privilege escalation blocks, `413` oversized body enforcement (`BodyLimitBytes`), CRLF header sanitization, production CORS/CSRF/Cookie flags, refresh token reuse family revocation, **26 SSRF & obfuscated IP vectors** (including octal/hex/dword IPv4, trailing dot, DNS-rebinding `SafeDialer.Control`), constant-time webhook HMAC-SHA256 verification, and `LogCapture` secret/PII leak scanning.

---

## 5. Live Chaos, Resilience & Load/Soak Verification (Parts 6 & 7)
- **Part 6 (`scripts/run-part6-resilience.ps1`)**:
  - Fail-fast `exit=1` startup rejection on bad production loopback/weak-JWT/non-loopback-`pprof` configs.
  - Brief DB TCP-proxy outage (`2.5s`): `/healthz` stayed `200`, `/readyz` transitioned `200 -> 503` (`database` down) and auto-recovered to `200` with zero restart.
  - Live Kafka KRaft stop (`stop-kafka.ps1`) and restart (`start-kafka.ps1`) mid-traffic: `/readyz` `200 -> 503 -> 200`, HTTP/SMPP traffic resumed cleanly.
  - HTTP carrier chaos (`502`, `429`, HTML/binary garbage, 15s slow stall), SMPP `0x58` throttling + 250ms latency, customer `429` (`Retry-After=1`), **`15/15` HTTP + `15/15` SMPP** delivered with **0 duplicate carrier sends** and `discrepancy_count=0`.
- **Part 7 (`docs/LOAD_TEST.md`)**: `100k` mixed HTTP+SMPP burst (`0` errors) + `5m` (`304s`) soak (`0` errors, flat RSS/goroutines/DB conns), plus clean post-fix `60k` burst (`316.3 msg/s` accepted, `60,001` messages reconciled with **`discrepancy_count=0` / `ledger_matches=true`**).

---

## 6. Database Migration Rollback & Backup/Restore Verification (Part 9)
- **Script**: `scripts/run-part9-dbverify.ps1`
- **Verified**: Rolled back **`14 -> 1`** and reapplied **`1 -> 14`** (`schema_migrations=14|false`) on **60,001-message populated `znero_test`**, re-seeded baseline, verified 100% `znero_app` ownership, executed `pg_dump` (`2018.1 KB`) -> `pg_restore` into fresh temporary database `znero_restore_tmp`, verified exact table counts & `0` unbalanced ledger transactions, booted live `znero.exe -role=api` (`:18090`), verified `/healthz=200`, `/readyz=200`, `X-Request-ID`, login, and `/messaging/reconciliation/run` (`discrepancy_count=0`, `ledger_matches=True`), then dropped `znero_restore_tmp`.

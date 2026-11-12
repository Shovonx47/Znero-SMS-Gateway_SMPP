# Znero International — Long Run Verification Log (`docs/RUN_LOG.md`)

---

## 2026-11-12 05:40 +06:00 — Part 0: Environment Started
- **Started**: Multi-process role refactoring (`cmd/api/main.go`), `bin\` binary builds, `scripts/check-db-ownership.ps1`, `scripts/dev-up.ps1`, `scripts/dev-down.ps1`, and verification against `znero_test` / read-only audit of `znero_dev`.
- **Implemented & Found**:
  - Added `-role` (`all|api|smpp|dispatcher|reaper|webhook|poller`) and loopback-only `-pprof-addr` to `backend/cmd/api/main.go` so each runtime service runs as a distinct OS process sharing `bin\znero.exe` with periodic cache refresh across processes.
  - Discovered `C:\Windows\system32\psql` is a 0-byte stub on this Windows host, causing naive `psql` invocations without `.exe` resolution to silently output empty results. Fixed `scripts/check-db-ownership.ps1` to resolve real `psql.exe` (`C:\Program Files\PostgreSQL\17\bin\psql.exe`) and check `pg_class.oid` DML privileges cleanly.
  - **DB Ownership & Migration Audit**:
    - `znero_test`: Migration `14|f`, **0** unowned objects, **100%** `znero_app` ownership & full DML privileges.
    - `znero_dev` (read-only check): Migration `12|f` (Migrations 13–14 pending), **6 Migration 11 tables still owned by `postgres`** without `znero_app` DML privileges (`messaging_reconciliation_runs`, `provider_connection_dlr_mappings`, `provider_connection_dlr_mappings_history`, `provider_connection_sessions`, `settlement_exceptions`, `unmatched_dlrs`). Left untouched per rules; will document remediation commands cleanly.
  - Tested `scripts/dev-up.ps1 -Target test -WithPprof` twice in a row (idempotent cleanup verified; fixed PowerShell `-Db` alias clash with `-Debug` -> `-Target`, and relative `vite.js` path under `-WorkingDirectory frontend` for paths with spaces). Confirmed all 8 processes (`kafka-kraft`, `znero-api` :8080, `znero-smpp` :2776, `znero-dispatcher`, `znero-reaper`, `znero-webhook`, `znero-poller`, `znero-frontend` :5173) `[RUNNING]`, `/readyz` ready, frontend `200`, and `scripts/dev-down.ps1 -KeepKafka` stops all cleanly. Part 0 **COMPLETE**.

## 2026-11-12 05:53 +06:00 — Part 1: Unblock Stage 7 & Expand Full 2x2x2 + Failure + Chaos E2E Suite
- **Started**: Root-causing `e2e_matrix_test.go:592` webhook timeout, stale `SP-Conn-*` routing dials, and post-failure teardown hang.
- **Found (first pass)**: `messaging/repository.go:1820` emits `"message." + params.ToStatus` (`message.delivered`, `message.undelivered`, `message.failed`, etc.), whereas `e2e_matrix_test.go` seeded `'["message.final"]'`, so `customer_webhooks` matched 0 rows.


- **Implemented & Fixed (Part 1 COMPLETE — `06:50 +06:00`)**:
  - Added explicit non-production `ALLOW_LOOPBACK_IN_TEST` switch (`config.Parse` fails fast if enabled in `production`, wired via `cfg.AllowLoopback()` into `cmd/api/main.go` and `scripts/dev-up.ps1 -Target test`; `BUG-024`).
  - Split `backend/internal/e2e` cleanly into `e2e_helpers_test.go`, `e2e_matrix_test.go`, and `e2e_failures_chaos_test.go` with clean-slate resets, active stale connection/plan deactivation, `TestClock`, `LogCapture`, and `watchGoroutineLeaks` (excluding only `fasthttp.updateServerDate` singleton).
  - Fixed **8 bugs (`BUG-018`..`BUG-025`)** uncovered by the expanded E2E & failure/chaos matrix:
    - `BUG-018`: `Reaper.retryUnmatchedDLRs` clock & HTTP structured DLR fallback (`messaging/reaper.go`).
    - `BUG-019`: `kafkaclient` nil-logger panic in background broker reconnect (`pkg/kafkaclient/kafkaclient.go`).
    - `BUG-020`: Fast immediate DLR arriving before `Dispatcher.handleAccepted` commits `StatusSubmitted` dropped silently (`messaging/dlr.go` retry + `unmatched_dlrs` buffer; `smpp/client.go` 1-roundtrip lookup).
    - `BUG-021`: `smpp.ConnectionManager` leaked `pgxpool` advisory lock connections on decrypt/dial/bind failure, hanging `pgxpool.Close()` on teardown (`smpp/manager.go`).
    - `BUG-022`: `pgRepository.UpdateMessageStatus` never enqueued `customer_webhook_deliveries` or `smpp_dlr_queue`, and `smpp_dlr_queue.error_code` lacked 16-char clamping (`messaging/repository.go`).
    - `BUG-023`: `webhook.Dispatcher.recordFailure` still wrote invalid `'retry'` status violating `chk_webhook_delivery_status`; fixed to keep `'pending'` + backoff and fixed `webhook_test.go` tenant org IDs and expected `'pending'` status.
    - `BUG-024`: Explicit `ALLOW_LOOPBACK_IN_TEST` guard.
    - `BUG-025`: `smpp.Session.Submit` / `smpp.Client.Send` misclassified post-write connection drops as `SendStatusRetryableReject`, permitting duplicate SMS failover; fixed via `ErrAmbiguousOutcome` -> `SendStatusUnknown`.
  - Updated two broken pre-existing test expectations (`webhook_test.go:148` `'retry'` -> `'pending'` + `custID` mismatch, `billing_dlr_reaper_test.go:270` stale `StatusUnknown` -> `StatusFailed` matching `reaper.go:258` and `models.go:78`).
- **Verified**: `go test -timeout=12m -p=1 -race -count=3 ./internal/config/... ./internal/webhook/... ./internal/smpp/... ./internal/messaging/... ./internal/e2e/...` **PASS** (100% green across all 3 runs, 0 data races, 0 goroutine leaks, 0 reconciliation discrepancies); `gofmt -w .` + `golangci-lint run ./...` **0 issues**.

## 2026-11-12 06:55 .. 08:56 +06:00 — Part 2: Full External OS-Process Black-Box Verification & SMPP/Crash Hardening
- **Started**: Built standalone mock HTTP provider (`cmd/e2ehttpprov`), expanded external SMPP SMSC (`cmd/smppmock`) and external REST/TCP/SMPP harness (`cmd/e2eblackbox`), independent third-party Python `smpplib` client (`scripts/test-smpplib-client.py`), and `scripts/run-part2-blackbox.ps1` orchestrating all 6 Znero service processes (`znero.exe -role=...`) + carrier simulators against `znero_test`.
- **Bugs Found & Fixed (`BUG-026`..`BUG-035`)**:
  - `BUG-026`: Added platform-only `POST /api/v1/wallets` (`EnsureWallet`) and mapped `ErrValidation` -> HTTP 400 (`ledger/{service,handler}.go`).
  - `BUG-027`: Implemented SMPP `submit_sm` `data_coding` decoding (`GSM-7` default + extension `'€'`, `UCS-2` UTF-16BE Bangla/Arabic/Emoji, `Latin-1`, rejecting binary `0x04` with `StatusRInvDCS`) in `smppserver/session.go`.
  - `BUG-028`: Added explicit non-production `ALLOW_LOOPBACK_IN_TEST` loopback opt-in to `provider/validation.go` (`SetAllowLoopback`) while keeping private/RFC1918/metadata/empty hosts blocked.
  - `BUG-029`: Eliminated multi-process fast-DLR race via bounded `8 x 15ms` `waitDLRRetryDelay` in `DLRProcessor.ProcessParsedDLR`, lookup error logging in `smpp/client.go`, `{}` default payload in `InsertSMPPDLRQueue`, and `2s` dev/test Reaper polling.
  - `BUG-030` & `BUG-031`: Fixed critical brute-force lockout flaw where `smppserver.BindLimiter.IsLockedOut` and `customeredge.EdgeAuthenticator.isLockedOut` deleted failure counters on attempt #2 (`lockedUntil.IsZero()`), plus added malformed-key lockout accounting.
  - `BUG-032`: Keyed SMPP rate limits per `s.AccountID` and updated `CustomerLimiter.AllowN` to dynamically resize `rate`/`capacity` & clamp `tokens` when `max_tps` changes.
  - `BUG-033`: Fixed SMPP v3.4 wire interop failure caught by Python `smpplib`: generated 8-char `[A-Za-z0-9]` SMPP passwords (`secrets.GenerateSMPPPassword()`), added `password.HashRaw` (keeping >=12-char enforcement on human `password.Hash`), and bounded `system_id` to `4..15` chars.
  - `BUG-034`: Stopped `smpp.Session` from replying `deliver_sm_resp` `StatusROK` on `DLRHandler` failure (`StatusRSysErr` instead), tuned `Dispatcher.lockDuration` to `15s`, and added `ReplayUnackedDLRs` on `mocksmsc`/`smppmock`.
  - `BUG-035`: Fixed at-most-once crash-recovery duplicate-send bug where `kill -9 znero-dispatcher` mid-`submit_sm` caused the restarting worker to re-send when the outbox lease expired. Added durable pre-send `StatusSubmitting` + `message_attempts` `'submitting'` checkpoint (`beginAttemptTx`) + in-place `RecordAttempt` finalization + locked `currentStatus` in `handleAmbiguousUnknown`.
- **Verified**:
  - `go vet ./...`, `-race` integration suites (`secrets`, `customer`, `ledger`, `provider`, `smppserver`, `customeredge`, `smpp`, `messaging` `82.962s` w/ `TestDispatcher_PreSendCheckpointAndCrashBlocksDuplicateSend`): **ALL PASS**.
  - `scripts\run-part2-blackbox.ps1` end-to-end (`task-12439`): REST setup + Maker-Checker (`403` self-approve, `200` reject), all 4 Matrix combinations (HTTP/SMPP x SMPP/HTTP, `on_delivery` & `on_provider_accept` + callback refund + HMAC webhooks + `deliver_sm`), SMPP edge cases (GSM-7 ext/160/161, UCS-2 70/71, Latin-1, binary `StatusRInvDCS`, 1-byte drip-feed, coalesced frames, UDH/SAR reject, garbage frame, lockout, `max_binds=1`, `max_tps=2`), Direct SQL ledger (`SUM(debit)==SUM(credit)` & wallet invariant), REST reconciliation (`checked=290..312, discrepancies=0`), Python `smpplib` bind + GSM-7/UCS-2 + `deliver_sm` DLRs + post-crash rebind, 4 `kill -9` crashes (`dispatcher`, `api`, `webhook`, `smpp`) + Graceful Drain (`18/18` delivered, `0` duplicates, `0` discrepancies): **ALL PASSED**. Part 2 **COMPLETE**.

## 2026-11-12 09:38 +06:00 — Part 3: Seeded Randomized State-Machine & Money Fuzz Suite Complete
- **Implemented & Fixed (`BUG-036`, `BUG-037`, `DEC-010`)**:
  - Built `backend/internal/e2e/e2e_fuzz_statemachine_test.go` running **22,000 randomized ops across 4 deterministic seeds** (`2026111201..2026111204`, 5,500 ops/seed) covering customer & reseller submits, multi-part messages, HTTP & SMPP paths, live profile billing-term mutations in-flight, provider/network/drop/timeout faults, out-of-order/duplicate/unmatched DLRs, clock advances & Reaper sweeps, EUR vs USD currency isolation, double-entry ledger balance checks, and full `ReconciliationService` audits.
  - Caught & fixed `BUG-036` (`reaper.go` leaving `'captured'` holds when `on_provider_accept + refund` expired past `dlr_wait_hours`) and `BUG-037` (`reconcile.go` conflating carrier `EXPIRED` DLRs with Reaper `capture_at_ttl`).
- **5-Mutation Verification (`seed_2026111201`, every temporary mutation proved to fail & cleanly reverted)**:
  1. Mutation 1 (skip capture): **FAIL** (`ORACLE MONEY MISMATCH DB(cached=100.00, active_holds=0.25) vs Oracle(cached=99.75, active_holds=0.00)`).
  2. Mutation 2 (double capture): **FAIL** (`DB(cached=97.15) vs Oracle(cached=97.50)`).
  3. Mutation 3 (refund instead of `keep_charge` on failed DLR): **FAIL** (`DIFF hold=refunded vs Oracle keep_charge`; `DB(cached=97.8500) vs Oracle(cached=97.5000)`).
  4. Mutation 4 (read live profile instead of message snapshot): **FAIL** (`ORACLE MONEY MISMATCH on fuzz-c2-deliv-cap-smpp: DB(cached=98.9500, active_holds=0.0500) vs Oracle(cached=99.1000, active_holds=0.0000)`).
  5. Mutation 5 (disable currency guard in `PlaceHoldTx`): **FAIL** (`CURRENCY MUTATION DETECTED: expected ErrCurrencyMismatch placing USD hold on EUR wallet, got <nil>`).
- **Verified**: `MUTATION_MATCHES=0`, `go vet ./...`, `-race` `internal/e2e` (`65.431s`), `internal/messaging` (`70.604s`), `internal/ledger` (`-p=1`, `16.280s`, noted `TestReconciliationRepair_PermissionsAuditAndImmutability` requires `-p=1` due to shared `znero_test` `ledger_entries` count), `gofmt -w .`, `go build ./...`, `golangci-lint run ./...` (**0 issues**). Part 3 **COMPLETE**.

## 2026-11-12 10:39 +06:00 — Part 4: Real-Browser Playwright E2E Suite Complete
- **Environment & Hygiene**:
  - Updated `frontend/e2e/phase3-masterdata.spec.ts` so DB cleanup/ISO probes strictly target `TEST_DATABASE_URL` (`znero_test`) rather than hardcoded `znero_dev`.
  - Added default ISO-4217 currency mapping for key countries in `backend/cmd/seedmastersample/main.go` so fresh clean DBs support `seedratessample` out of the box; cleanly migrated (`0 -> 14`) and bootstrapped/seeded `znero_test`, and verified 100% `znero_app` ownership (`check-db-ownership.ps1 -Target test -FailOnMismatch`).
  - Fixed `phase6-wallets-billing.spec.ts` #4 to dynamically provision an isolated foreign customer wallet instead of querying an absent hardcoded UUID.
- **Bugs Found & Fixed (`BUG-038`..`BUG-045`)**:
  - `BUG-038`: Down migrations `000004`, `000007`, `000008`, `000010`, `000012` failed when rolling back populated databases due to `change_requests` / `import_jobs` CHECK constraints; added out-of-scope row cleanup prior to re-constraining.
  - `BUG-039`: `routing.Engine.Resolve` serialized nil `candidates` / `rules_applied` slices as JSON `null`, crashing `routing/simulator/+page.svelte` in Svelte 5 and freezing it on `"Simulating..."`.
  - `BUG-040`: `routing.Service.SnapshotStatus` lacked `age_ms` expected by `routing/simulator/+page.svelte` (`"• ms old"`).
  - `BUG-041`: Added missing `aria-label` attributes on 45 unlabeled `<select>` / `<input>` controls across 22 Svelte route files.
  - `BUG-042`: Added missing `|| []` fallbacks on `res.data.items` assignments across 10 Svelte pages to eliminate null-array crashes on empty tables.
  - `BUG-043`: Fixed `/reconciliation` UI (`client.ts` called 404 `/api/v1/ledger/reconcile` instead of `GET /api/v1/ledger/reconciliation` and `POST /api/v1/ledger/reconciliation/run` with `{"repair": true}`).
  - `BUG-044` & `BUG-045`: Gated auxiliary `listOrganizations` / `listCountries` / `listAllConnections` / API-key / SMPP / Sender-ID dropdown requests on `auth.hasPermission(...)` across `/customers`, `/routing/assignments`, `/routing/simulator`, `/sender-ids`, and `/messages`.
- **Verified**:
  - `npm run check` (`svelte-check` 0 errors, 0 warnings), `npm test` (vitest 6/6 pass), `go build ./...`, `go vet ./...`.
  - Full Playwright Chromium E2E suite against live 8-process `dev-up.ps1 -Target test`: **`53 passed (2.7m)`** (`auth-guard`, `phase3-masterdata`, `phase4-providers-customers`, `phase5-rates`, `phase6-wallets-billing`, `phase7-routing`, `part4-comprehensive` covering all 8 RBAC roles across 41 routes with 0 console errors / 0 pageErrors / 0 5xx / 0 A11y issues / 0 horizontal overflow, live loopback HTTP provider Test Send + SSRF negative check + secret masking, per-customer billing terms Maker-Checker + history + reseller block, API key reveal-once, auth lockout, responsive `390px`/`768px` drawers, customer portal zero-leak & IDOR URL blocks; screenshots saved in `docs/e2e-artifacts/`). Part 4 **COMPLETE**.

## 2026-11-12 11:09 +06:00 — Part 5: Security Audit & Hardening Complete
- **Security Checks & Dependency Audits**:
  - `npm --prefix frontend audit`: reported 9 known issues (`6 low` in `cookie` / `@sveltejs/kit`, `1 moderate` / `2 critical` in `devDependencies` `vitest`/`tinypool` whose remediation requires breaking major upgrades `@sveltejs/kit@3.2.0` / `vitest@5.1.0`; 0 runtime production dependencies flagged `high`/`critical`).
  - `govulncheck` / `gosec` binary check: not installed locally (`Get-Command` found `golangci-lint.exe` only); static security rules covered by `golangci-lint run ./...` + manual code audit + dedicated runtime hardening tests.
- **Bugs Found & Fixed (`BUG-046`..`BUG-048`)**:
  - `BUG-046`: `system.info.view` was missing from `rbac.platformOnlyExact` (causing 38 stale grants across non-platform `Staff`/`Read-only` roles in `znero_test`) and `app/app.go` mounted `GET /api/v1/system/info` without `rbac.RequirePermission("system.info.view")`. Added `system.info.view` to `platformOnlyExact`, enforced permission middleware on `/api/v1/system/info`, and executed idempotent `scripts/repair-legacy-rbac-permissions.sql` against `znero_test` (`38` rows deleted -> `0` remaining; `znero_dev` untouched per rules).
  - `BUG-047`: `customeredge` webhook registration/update (`POST`/`PUT /api/v1/customer/webhooks`) previously accepted any `http://`/`https://` prefix with zero SSRF/control-char/status validation. Added `EdgeHandler.SetAllowLoopback(env, allow)` (fail-closed in production) + `provider.ValidateHTTPBaseURLWithOptions` + CRLF + status validation, plus `provider/ssrf_safety.go` rejecting trailing-dot IPs (`127.0.0.1.`) and obfuscated C-integer IPv4 forms (`0177.0.0.1`, `0x7f000001`, `2130706433`, `127.1`).
  - `BUG-048`: `DELETE /api/v1/customer/webhooks/:id` returned `204` and `GET /api/v1/customer/webhooks/:id/deliveries` returned `200` on non-owned webhooks (`items: null`). Added `GetWebhook` ownership verification returning `404` and non-nil slice initialization. Also fixed pre-unification `"zn_live_"` prefix and `201 Created` status check in `customeredge_test.go`.
  - Normalized cross-tenant `"user not found"` on `auth` suspend/activate/role-update handlers (`backend/internal/auth/handler.go`) to return `404 Not Found`.
- **Verified (`-count=1 -p=1 -race` against `znero_test`)**:
  - `./internal/auth/...` (`5.427s`), `./internal/rbac/...` (`1.524s`), `./internal/provider/...` (`1.974s`), `./internal/customeredge/...` (`1.679s`), `./internal/webhook/...` (`1.366s`), `./internal/app/...` (`8.920s` — `385`-route RBAC matrix + new `security_hardening_test.go` covering cross-tenant IDOR across Reseller A/Cust A1 vs Reseller B/Cust B1, SQLi, privilege escalation, TCP `BodyLimitBytes` `413`, CRLF sanitization, production CORS/CSRF/Cookie flags/Refresh token reuse family revocation, 26 SSRF & obfuscated IP vectors, DNS rebinding `SafeDialer`, real HMAC-SHA256 verification, and `LogCapture` leak scan): **ALL PASS**.
  - `gofmt -w .`, `go vet ./...`, `go build ./...`, `golangci-lint run ./...`: **PASS (0 issues)**. Part 5 **COMPLETE**.

## 2026-11-12 11:19 +06:00 — Part 6: Live OS-Process Resilience Suite Complete
- Added [part6resilience/main.go](file:///e:/Softcents%20Systems/Znero%20International/backend/cmd/part6resilience/main.go) and [run-part6-resilience.ps1](file:///e:/Softcents%20Systems/Znero%20International/scripts/run-part6-resilience.ps1) and ran all scenarios against real built OS processes (`znero.exe` across all 6 roles, `znero-smppmock.exe`, `znero-e2ehttpprov.exe`, Kafka KRaft, PostgreSQL `znero_test`):
  1. **Fail-Fast Bad Configs**: verified immediate `exit=1` rejection for `APP_ENV=production` + `ALLOW_LOOPBACK_IN_TEST=true`, short `JWT_ACCESS_SECRET`, and non-loopback `-pprof-addr=0.0.0.0:6060`.
  2. **Brief DB Outage + `/healthz` vs `/readyz` Auto-Recovery**: ran an isolated `znero-api` OS process behind a loopback TCP proxy; severed connections/forwarding for 2.5s -> `/healthz` remained `200`, `/readyz` returned `503` reporting `database` down, and `/readyz` auto-recovered to `200` within seconds after proxy restoration without restart.
  3. **Kafka Outage & Restart Mid-Load**: stopped live Kafka (`stop-kafka.ps1`, PID 15960) during active HTTP + SMPP traffic -> `/healthz` stayed `200`, `/readyz` transitioned `200 -> 503`, queued HTTP + SMPP batches continued cleanly -> restarted Kafka (`start-kafka.ps1`, PID 22060) -> `/readyz` auto-recovered to `200`.
  4. **Flapping / Garbage / Slow Provider Chaos + `429` Backpressure**: injected `502`, `429`, `text/html`, `application/octet-stream` binary, and 15s `slow` stalls on `e2ehttpprov`, plus SMPP `0x58` + 250ms latency on `smppmock`, restored healthy carriers, triggered deterministic customer TPS `429` (`Retry-After=1`), and verified **0 duplicate external carrier sends** (`15/15` HTTP + `15/15` SMPP), balanced SQL double-entry & wallet invariants, and **`discrepancy_count=0` / `ledger_matches=true`** via HTTP reconciliation. Part 6 **COMPLETE**.
## 2026-11-12 12:30 +06:00 — Part 7 & Part 8: Mixed Load, `pprof` Optimization & Code-Review Fixes Complete
- **Part 8 Targeted Code Review & Fixes (`BUG-049`..`BUG-052`)**:
  - Added bounded `maxWorkers = 8` + `batchSize=64` + `pollInterval=25ms` continuous drain in `messaging/dispatcher.go`.
  - Fixed `BUG-049` (`webhook/dispatcher.go`: preserve worker `ctx` instead of expired HTTP `reqCtx` when persisting timeout delivery attempts; added regression test in `webhook_test.go`).
  - Fixed `BUG-050` (`messaging/reaper.go`: added `defer tx.Rollback(ctx)` + explicit error checks in extracted single-item reaper helpers `reapSingleStuck`, `reapSingleUnknown`, `reapSingleNoDLR`).
  - Fixed `BUG-051` (`smpp/manager.go`: per-connection singleflight `bindLocks` eliminating cold-start SMPP carrier bind stampedes).
  - Fixed `BUG-052` (found via live `pprof`: demoted 3 per-message synchronous `slog.Info` calls in `messaging/service.go`, `dispatcher.go`, and `dlr.go` to `Debug`, cutting `smpp_cpu` slog overhead from `81.92% -> 0%` and `api_cpu` `FD.Write` from `88.11% -> 51.25%`).
  - Fixed `BUG-053` (`messaging/reconcile.go`: include `status = 'unknown'` for submitted `on_provider_accept` messages in `expectedCapturedTotal`; added `TestReconciliation_OnProviderAcceptUnknownDLR`) and `BUG-054` (`cmd/loadgen/main.go`: canonical non-terminal status filter).
- **Part 7 Mixed Load, `pprof`, Soak & Ledger Audit (`docs/LOAD_TEST.md`)**:
  - **100k Burst + 5m Soak (`run-part7-load.ps1 -SoakDuration 5m`)**: `100,000 / 100,000` accepted in `421.64s` (`237.2 msg/s`, `errs=(0,0,0)`); `5m` (`304s`) soak sent `7,356` mixed msgs with `0` errors and flat RSS, goroutines (`api 42->31`, `dispatcher 13->13`, `smpp 14->14`), and DB connections (`41->41`).
  - **Clean 60k Burst Post-Fix (`-HttpSingle 15000 -HttpBulkBatches 300 -BulkSize 100 -SmppCount 15000`)**: `60,000 / 60,000` accepted in `189.69s` (**`316.3 msg/s`**, `errs=(0,0,0)`); measured async E2E drain at `~63.8 msg/s` (`33,781` drained in bounded 8m drain window); post-load audit (`znero-loadgen.exe -mode=verify`) verified balanced SQL ledger/wallets and `messages_checked=60001 discrepancy_count=0 ledger_matches=true`.

## 2026-11-12 12:34 +06:00 — Part 9: Migration `14->1->14`, `pg_dump`/`pg_restore`, Docs & Compliance Complete
- **Executed `scripts/run-part9-dbverify.ps1`**:
  - Tested `migrate down 13` -> `1` -> `migrate up` -> `14|false` on `60,001`-message populated `znero_test`.
  - Cleanly re-bootstrapped & re-seeded baseline sample data, ran `repair-legacy-rbac-permissions.sql`, and verified `check-db-ownership.ps1 -Target test -FailOnMismatch` (`14|f`, 100% `znero_app`).
  - Executed `pg_dump` (`2018.1 KB`) -> `pg_restore` into `znero_restore_tmp`, verified row-count parity, `0` non-`znero_app` tables, `0` unbalanced ledger TXs, and booted live `znero.exe -role=api` (`:18090`) verifying `/healthz=200`, `/readyz=200`, `X-Request-ID`, login, and `/messaging/reconciliation/run` (`discrepancy_count=0`, `ledger_matches=True`), then dropped `znero_restore_tmp`.
- Authored `docs/RUNBOOK.md`, `docs/SECURITY_REVIEW.md`, `docs/LOAD_TEST.md`, `docs/E2E_REPORT.md`, `docs/SPEC_COMPLIANCE.md`, and `DEC-011` in `docs/DECISIONS.md`.

## 2026-11-12 12:49 +06:00 — Part 10: Final Full Sweep & Clean Shutdown Complete
- Fixed `BUG-055` (`TRUNCATE TABLE message_events, message_dlrs` before clean-slate deletes in `e2e_helpers_test.go` so `go test ./...` passes regardless of cross-package DB rows left in append-only tables).
- Verified `OI-001` hardened watchlist paths (`SetAllowLoopback`, `reapSingleStuck`, `bindLocks`), `znero_restore_tmp` absent, and ran final full verification suite after all edits:
  - `gofmt -w .`, `go vet ./...`, `go build ./...`, `golangci-lint run ./...`: **PASS (0 issues)**.
  - `ZNERO_INTEGRATION=1 go test -count=1 -timeout=12m -p=1 -race ./...` against `znero_test`: **42 / 42 testable Go packages `ok`** (0 failures, 0 races).
  - `npm --prefix frontend run check`: **0 errors, 0 warnings**; `npm --prefix frontend test -- --run`: **6/6 passed**.
  - `scripts/check-db-ownership.ps1 -Target test -FailOnMismatch`: **PASS (`14|f`, 100% `znero_app`)**.
- Stopped Kafka & any background Znero processes cleanly via `scripts/dev-down.ps1 -Quiet`. Published `docs/OVERNIGHT_REPORT_2.md`.

## 2026-11-12 15:19 +06:00 — Phase 9 Part 1: Isolated >=120m Background Soak Started
- Built copied standalone binaries (`bin\soak\znero-soak.exe`, `bin\soak\znero-soakrunner.exe`) and created isolated database `znero_soak` (`znero_dev` untouched; `znero_test` kept free for Parts 2–7).
- Started 6 separate OS processes (`api` PID `4748` `:18180`, `smpp` PID `24272` `:12776`, `dispatcher` PID `5420`, `reaper` PID `11068`, `webhook` PID `13956`, `poller` PID `22772`, `pprof` `:16060..:16065`) + `soakrunner` (`-duration=125m -sample-interval=5m -tps=50`, 6 customers across HTTP+SMPP upstreams and all billing policies, webhook chaos, delayed/failed/no-receipt DLRs, occasional upstream hangs) with PIDs saved in `.run\soak\pids.json`.
- Verified `t=0.1m` baseline sample in `docs/soak-telemetry.jsonl`: `http=124(err=0) smpp=122(err=0) db=(1/22) p50=6.5ms p95=60.1ms invOK=true`.

## 2026-11-12 18:20 +06:00 — Phase 9 Stage 0: Preflight, Baseline & Triage Complete
- **Compiler, Linters & IDE Triage**:
  - `go build ./...`, `go vet ./...`, `golangci-lint run ./...` all clean (0 issues). Fixed SA9003 empty condition in `cmd/soakrunner/main.go`.
  - Frontend `npm run check` (0 errors, 0 warnings), `npx tsc --noEmit` (0 errors).
  - Tamed editor problems via `.markdownlint.json` and `.vscode/settings.json` directory exclusions (`-frontend`, `-node_modules`, `-.tmp`, `-.run`, `-bin`, `-e2e-artifacts`).
- **Core Deliverables Added**:
  - `backend/internal/metrics/registry.go` & `registry_test.go`: Zero-dependency, Prometheus 0.0.4 text format metrics registry with loopback/`METRICS_TOKEN` auth and zero PII.
  - `ops/alerts.yml`: Prometheus alert rules for process health, reconciliation, outbox backlog, failure rate, and delivery backlogs.
  - `scripts/security-scan.ps1`: Automated security scanner running `govulncheck`, `gosec`, and `npm audit`.
  - `scripts/upgrade-dev-database.ps1`: Automated operator database upgrade utility with pre-upgrade `pg_dump` backup, migration execution, ownership enforcement (`znero_app`), and explicit rollback instructions.
  - `docs/PILOT_CHECKLIST.md`: External SMS provider onboarding checklist.
  - `docs/DECISIONS.md`: Added `DEC-012` confirming safe default policies.
- **Full Baseline Integration Test Suite Run**:
  - `scripts\test.ps1 -Integration`: All 35 backend packages passed (including 22,000-op state machine & money fuzz suite across 4 seeds); frontend unit tests 6/6 passed; frontend svelte-check 0 errors.

## 2026-11-12 18:25 +06:00 — Phase 9 Stage 1: Design Document First Complete
- Authored comprehensive `docs/DASHBOARD_DESIGN.md` establishing:
  1. Source of truth: Realized revenue linked directly to ledger capture entries (`type = 'hold_capture'`); pending holds reported separately; estimated buy cost; margin per matching currency.
  2. Event-time semantics: Bucketing by event occurrence time for historical immutability.
  3. Rollup fact tables: 15-minute base buckets for exact global timezone realignment (including UTC+5:30 and UTC+5:45). Split into `analytics_rollup_customer_15m`, `analytics_rollup_provider_15m`, and `analytics_rollup_destination_15m` with monthly partitions.
  4. Exactly-once durable consumption: `analytics_cursors` updated in the same database transaction as rollup increments.
  5. Dedicated runtime role `-role=analytics` with PostgreSQL advisory lock `pg_try_advisory_lock(8899001)`.
  6. Multi-tenant SQL projection boundaries: Strict server-side projection separation.

## 2026-11-12 19:17 +06:00 — Phase 9 Stage 2: Data Foundation & Rollup Engine Complete
- **Migration 000015 (`000015_phase9_visibility_foundation.up.sql` & `.down.sql`)**:
  - Implemented `analytics_cursors` (high-water mark durability).
  - Implemented partitioned 15-minute rollup fact tables: `analytics_rollup_customer_15m`, `analytics_rollup_provider_15m` (platform-only), and `analytics_rollup_destination_15m` with monthly partitions (`2026_11`, `2026_12`, `2027_01`, `default`).
  - Added operational & visibility tables: `service_heartbeats`, `notifications`, `notification_preferences`, `alert_rules`, `alerts`, `incidents`, `incident_timeline`, `settings_registry`, `saved_filters`, `user_preferences`, and `export_jobs`.
  - Registered Phase 9 permissions with strict RBAC (`platformOnlyPrefixes` updated with `alert.`, `incident.`, and `platformOnlyExact` updated with `analytics.noc.view`, `setting.manage`).
  - Verified `000015` up/down/reapply in `migrations_test.go` (`4.43s` PASS) and 100% `znero_app` ownership on `znero_test` (`15|f`).
- **Rollup Engine Implementation (`backend/internal/analytics/`)**:
  - `models.go`: Typed domain models for rollups, heartbeats, filters, and dashboard projections.
  - `histogram.go`: Fixed-boundary latency histogram with percentile linear interpolation (p50, p95, p99).
  - `repository.go`: Atomic cursor lock + event stream ingestion from `message_events` + joined `messages`, upserting into 15m rollups with exactly-once cursor advance in a single transaction. E.164 country resolution via libphonenumber. Time series queries realigned across arbitrary timezones.
  - `worker.go`: Background rollup loop protected by PostgreSQL session advisory lock `pg_try_advisory_lock(8899001)`, with automatic leader election and standby backoff.
  - `service.go`: Tenant boundary sanitization, 2-second broadcast cache for O(1) live view queries, and server-side stripping of platform-only cost/margin for non-platform users.
  - `handler.go`: Fiber routes mounted under `/api/v1/analytics/`.
- **Process Orchestration & Wiring**:
  - Added `-role=analytics` to `backend/cmd/api/main.go` and wired `Analytics` module into `app.Modules`.
  - Added `znero-analytics` component to `scripts/dev-up.ps1` and `scripts/dev-down.ps1`.
- **Verification Suite**:
  - `internal/analytics`: 6/6 tests PASS (`TestLatencyHistogram`, `TestResolveCountryISO`, `TestAnalyticsEngine_DifferentialAndTieOut`, `TestAnalyticsTimezoneRealignment`, `TestAnalyticsWorkerAdvisoryLock`, `TestAnalyticsCrashAndResumeCursor`).
  - Linter & Compiler: `go build ./...` (0 errors), `go vet ./...` (0 errors), `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `npm run check` (0 errors, 0 warnings), `npx tsc --noEmit` (0 errors).

## 2026-11-12 19:30 +06:00 — Phase 9 Stage 3: Analytics APIs, Reports & Widget Registry Complete
- **API Endpoints Built & Mounted (`/api/v1/analytics/`)**:
  - `GET /dashboard/executive`: Platform view includes cost/margin; reseller/customer view strips buy cost and margin server-side.
  - `GET /dashboard/noc`: Platform-only NOC operational overview (ingress/egress TPS, outbox depth, active carrier connections, 24h success, alerts, incidents, heartbeats). Returns 403 Forbidden to non-platform tenants.
  - `GET /dashboard/reseller`: Reseller-scoped customer volume breakdown and active customer counts.
  - `GET /dashboard/customer`: Customer self-service spent vs balance remaining and delivery success rate.
  - `GET /noc/live`: 2-second broadcast-cached rolling telemetry (flat O(1) database cost).
  - `GET /reports/delivery`: Destination country and operator delivery rate and volume breakdown.
  - `GET /reports/financial`: Customer-level revenue captured, held, and refunded breakdown.
  - `GET /widgets`: Dynamic widget catalog scoped by user role.
  - `GET /preferences/layout` & `PUT /preferences/layout`: User customizable dashboard grid layout persistence.
  - `GET /heartbeats`: Runtime service heartbeats.
  - `POST /rebuild`: Event stream replay mechanism.
- **Verification & Budgets**:
  - `authz_matrix_test.go`: All 398 routes crawled across 5 principals cleanly (0 unauthorized leaks).
  - Tenancy leak scan: Zero exposure of `cost_by_currency`, `margin_by_currency`, `cost`, or `margin` in customer/reseller payloads.
  - Performance budgets: Executive dashboard p95 = **1.13ms** (budget: <300ms); Live NOC p95 = **689.6µs** (budget: <100ms).
  - Linter & Compiler: `go build ./...` (0), `go vet ./...` (0), `gofmt -l .` (0), `golangci-lint run ./...` (0), `npm run check` (0), `npx tsc --noEmit` (0).

## 2026-11-12 19:46 +06:00 — Phase 9 Stage 4: Live Hub (WebSocket, Real-time TPS) Complete
- **WebSocket Hub Implementation (`backend/internal/analytics/livehub.go`)**:
  - Full RFC 6455 compliance without external dependencies (pure Go standard library + FastHTTP hijack).
  - ComputeAcceptKey SHA1-base64 handshake calculation.
  - Text framing (0x81) and control framing (Ping 0x9, Pong 0xA, Close 0x8).
  - 5-second authentication timeout: clients connecting without token must send `{"type":"auth","token":"..."}` within 5s or be dropped with close code 4401.
  - Broadcast engine: 1-second background tick pulling cached Live NOC data (O(1) query) and fanning out role-filtered payloads.
  - Slow consumer defense: non-blocking channel send drops frames if client socket buffers back up, protecting server memory.
  - Tenancy isolation: Platform receives outbox depth, firing alerts, active connections; Reseller receives customer volume; Customer receives only self delivery rate. Zero buy cost, provider, or platform internal metrics leaked.
- **Verification Suite**:
  - `TestStage4_RFC6455_FramingAndHandshakeKey`: PASS (0.00s).
  - `TestStage4_LiveHub_AuthTimeoutAndTenantIsolation`: PASS (6.56s) - verified 5s auth timeout kick, greeting frames, role-specific telemetry fanout, zero tenancy leaks.
  - Linters & Compilers: `go build ./...` (0 errors), `go vet ./...` (0 errors), `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `npm run check` (0 errors), `npx tsc --noEmit` (0 errors).

## 2026-11-12 20:23 +06:00 — Phase 9 Stage 6: Alert Engine, Incidents, Insights Complete
- **Documentation First (`docs/ALERTING.md`)**:
  - Full specification of alert rules catalog, hysteresis, silence windows, incident state machine, and timeline audit rules.
- **Alert & Incident Engine (`backend/internal/alerting/`)**:
  - `models.go`: Alert rules, firing alerts, incidents, timeline entries, and strict state transition validator (`open -> investigating -> identified -> monitoring -> resolved`).
  - `repository.go`: PostgreSQL operations for `alert_rules`, `alerts`, `incidents`, `incident_timeline` with pagination and filtering.
  - `evaluator.go`: Metric evaluation engine supporting DB-backed telemetry and injectable `MockMetricSource` for testing.
  - `service.go`: Rule evaluation loop, anti-flapping idempotency, auto-resolution on metric normalization, silence management, and incident declaration/transition with timeline logging.
  - `handler.go`: Mounted under `/api/v1/alerts` and `/api/v1/incidents`:
    - Alert rules CRUD, list alerts, silence alert, trigger manual evaluation.
    - Declare incident, list incidents, get incident detail with timeline, transition incident status, add timeline entry.
- **Wiring & Integration**:
  - Wired into `app.Modules`, `cmd/api/main.go`, and connected to `notification.Service`.
- **Verification Suite**:
  - `TestAlertRuleLifecycleAndEvaluation`: PASS (0.08s) - verified rule creation, breach detection, anti-flapping idempotency, silencing, and auto-resolution.
  - `TestIncidentStateMachineAndTimeline`: PASS (0.06s) - verified invalid transition rejection (`open -> resolved` fails), full lifecycle transition, root cause recording, and ordered timeline entries.
  - `TestAlertingAndIncidents_RBACAndTenantIsolation`: PASS (0.06s) - verified customer receives 403 Forbidden on platform alerts and incidents.
  - `TestAuthzMatrix_AllRoutesAcrossFivePrincipals`: PASS (5.32s) - audited all **420** unique routes across 5 principals cleanly.
  - Linters & Compilers: `go build ./...` (0 errors), `go vet ./...` (0 errors), `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `npm run check` (0 errors), `npx tsc --noEmit` (0 errors).

## 2026-11-12 20:07 +06:00 — Phase 9 Stage 5: Notification Center Complete
- **Multi-Channel Delivery & Storm Protection (`backend/internal/notification/`)**:
  - `models.go`: Notification schemas, category preferences, and dispatch events.
  - `sender.go`: `EmailSender` (standard library SMTP with MIME multipart HTML/text), `WebhookSender` (HTTP client with HMAC-SHA256 signature `X-Znero-Signature: sha256=<hex>`), and thread-safe mocks for testing.
  - `suppressor.go`: `StormSuppressor` with 5-minute deduplication window and per-user sliding-window rate limiting (max 50 notifications/min).
  - `repository.go`: PostgreSQL operations for `notifications` and `notification_preferences` with strict tenant isolation.
  - `service.go`: Notification dispatch engine, multi-channel preferences routing, mark read, mark all read, archive, and test notification dispatch.
  - `handler.go`: Mounted under `/api/v1/notifications`:
    - `GET /`: Paginated inbox with category, severity, and read status filters.
    - `GET /unread-count`: Badge counter.
    - `PUT /:id/read`: Mark single notification as read.
    - `POST /read-all`: Mark all as read.
    - `DELETE /:id`: Delete notification.
    - `GET /preferences`: Get user notification preferences.
    - `PUT /preferences`: Update user notification preferences.
    - `POST /test`: Multi-channel test dispatch.
- **Wiring & Integration**:
  - Wired into `app.Modules` and `cmd/api/main.go`.
- **Verification Suite**:
  - `TestNotification_StormSuppressionAndRateLimiting`: PASS (0.00s) - verified 5m dedup window and 50/min per-user cap.
  - `TestNotification_CRUDAndTenantIsolation`: PASS (0.96s) - verified zero tenant leak between User A and User B.
  - `TestNotification_MultiChannelDispatch`: PASS (0.09s) - verified in-app, SMTP mock, and Webhook mock with HMAC signature.
  - `TestNotification_HTTPRoutesAndRBAC`: PASS (0.13s) - verified 401 unauth, 403 on missing `notification.manage`, 200 OK on full perms.
  - `TestAuthzMatrix_AllRoutesAcrossFivePrincipals`: PASS (4.01s) - audited all **408** unique routes across 5 principals cleanly.
  - Linters & Compilers: `go build ./...` (0 errors), `go vet ./...` (0 errors), `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `npm run check` (0 errors), `npx tsc --noEmit` (0 errors).

## 2026-11-12 20:23 +06:00 — Phase 9 Stage 6: Alert Engine, Incidents, Insights Complete
- **Documentation First (`docs/ALERTING.md`)**:
  - Full specification of alert rules catalog, hysteresis, silence windows, incident state machine, and timeline audit rules.
- **Alert & Incident Engine (`backend/internal/alerting/`)**:
  - `models.go`: Alert rules, firing alerts, incidents, timeline entries, and strict state transition validator (`open -> investigating -> identified -> monitoring -> resolved`).
  - `repository.go`: PostgreSQL operations for `alert_rules`, `alerts`, `incidents`, `incident_timeline` with pagination and filtering.
  - `evaluator.go`: Metric evaluation engine supporting DB-backed telemetry and injectable `MockMetricSource` for testing.
  - `service.go`: Rule evaluation loop, anti-flapping idempotency, auto-resolution on metric normalization, silence management, and incident declaration/transition with timeline logging.
  - `handler.go`: Mounted under `/api/v1/alerts` and `/api/v1/incidents`:
    - Alert rules CRUD, list alerts, silence alert, trigger manual evaluation.
    - Declare incident, list incidents, get incident detail with timeline, transition incident status, add timeline entry.
- **Wiring & Integration**:
  - Wired into `app.Modules`, `cmd/api/main.go`, and connected to `notification.Service`.
- **Verification Suite**:
  - `TestAlertRuleLifecycleAndEvaluation`: PASS (0.08s) - verified rule creation, breach detection, anti-flapping idempotency, silencing, and auto-resolution.
  - `TestIncidentStateMachineAndTimeline`: PASS (0.06s) - verified invalid transition rejection (`open -> resolved` fails), full lifecycle transition, root cause recording, and ordered timeline entries.
  - `TestAlertingAndIncidents_RBACAndTenantIsolation`: PASS (0.06s) - verified customer receives 403 Forbidden on platform alerts and incidents.
  - `TestAuthzMatrix_AllRoutesAcrossFivePrincipals`: PASS (5.32s) - audited all **420** unique routes across 5 principals cleanly.
  - Linters & Compilers: `go build ./...` (0 errors), `go vet ./...` (0 errors), `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues), `npm run check` (0 errors), `npx tsc --noEmit` (0 errors).




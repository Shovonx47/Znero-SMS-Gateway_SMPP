# Znero International — Operations & Production Runbook (`docs/RUNBOOK.md`)

Operational procedures for running, monitoring, migrating, backing up, and troubleshooting the Znero International multi-process backend and SvelteKit portal.

---

## 1. Service Topology & Process Roles

All backend roles compile into a single Go binary (`bin\znero.exe`, built from `backend/cmd/api`) and are separated via `-role=<role>` (or `ZNERO_ROLE`):

| Process Name | Flag / Role | Default Port / Transport | Responsibilities |
| :--- | :--- | :--- | :--- |
| **API Edge** | `-role=api` | HTTP `127.0.0.1:8080` (`APP_HOST:APP_PORT`) | Dashboard REST API (`/api/v1/...`), Customer Edge HTTP API (`/api/v1/customer/...`), HTTP Provider DLR Callbacks (`/api/v1/provider-callbacks/dlr/:connectionId`), `/healthz`, `/readyz` |
| **Customer SMPP Gateway** | `-role=smpp` | TCP `127.0.0.1:2776` (`CUSTOMER_SMPP_ADDR`) | Downstream ESME binds (`bind_transceiver`, `bind_transmitter`, `bind_receiver`), `submit_sm` ingestion, outbound `deliver_sm` DLR delivery |
| **Core Dispatcher** | `-role=dispatcher` | Outbox worker + persistent upstream TCP | Leases `message_outbox` rows (`FOR UPDATE SKIP LOCKED`), dynamic kill-switch/routing checks, upstream SMPP persistent binds & HTTP provider submission |
| **Safety Reaper** | `-role=reaper` | Periodic worker (PG advisory lock `8888/1001`) | Expires stuck messages (`MessageTTL`), `unknown` exposures (`UnknownResolutionWindow`), no-DLR wait windows (`dlr_wait_hours_snapshot` + `no_dlr_policy_snapshot`), retries unmatched DLRs |
| **Webhook Dispatcher** | `-role=webhook` | Outbound HTTP worker | Polls `customer_webhook_deliveries`, signs payloads with HMAC-SHA256 (`X-Znero-Signature`), retries with exponential backoff (30s–15m), SSRF-safe dialer |
| **HTTP DLR Poller** | `-role=poller` | Outbound HTTP worker | Polls active `http_dlr_mode='polling'` connections within an 8-day partition-pruned window |
| **Frontend Portal** | SvelteKit / Vite | HTTP `127.0.0.1:5173` | Operations & tenant admin portal (`frontend/`) |

### Local Dev / Test Orchestration
- **Start stack**: `.\scripts\dev-up.ps1 -Target dev` (or `-Target test` for `znero_test`). Optional flags: `-NoFrontend`, `-WithPprof` (binds loopback pprof ports `:6060` API, `:6061` SMPP, `:6062` Dispatcher), `-SkipBuild`.
- **Stop stack cleanly**: `.\scripts\dev-down.ps1` (pass `-KeepKafka` to leave the KRaft broker running).
- **Start / Stop Kafka alone**: `.\scripts\start-kafka.ps1`, `.\scripts\stop-kafka.ps1`.

---

## 2. Mandatory Environment & Production Safety Controls

Configuration is validated fail-fast at startup in `backend/internal/config/config.go` and `backend/cmd/api/main.go`. The process exits non-zero immediately if any invariant fails:
- `APP_ENV=production`:
  - `ALLOW_LOOPBACK_IN_TEST=true` is **strictly rejected at boot** (fail-fast exit). Even via internal API flags, `AllowLoopback()` evaluates `false` when `APP_ENV=production`.
  - `LOG_FORMAT` defaults to structured `json`.
  - Refresh cookies force `Secure: true`, `HttpOnly: true`, `SameSite: Strict`.
  - Development diagnostic endpoints (`/api/v1/dev/kafka/test-message`) and UI simulation test-send modes are completely unmounted/disabled.
  - Legacy `@dev.znero.test` accounts must never exist in production (critical warning logged at boot if detected).
- **Required secrets / lengths**:
  - `JWT_ACCESS_SECRET` (`>= 32` chars), `MFA_ENCRYPTION_KEY` (64 hex chars / 32 bytes), `SECRETS_ENCRYPTION_KEY` (AES-256-GCM key for stored provider passwords, API secrets, and webhook signing keys; supports rotation via `SECRETS_KEY_ID` + `SECRETS_ENCRYPTION_KEYS_PREVIOUS`).
  - `CORS_ALLOWED_ORIGINS`: explicit origin list only; wildcards `*` are rejected.
  - `-pprof-addr`: refused at boot unless bound strictly to loopback (`127.0.0.1` / `::1` / `localhost`).

---

## 3. Health, Readiness & Observability

### Probes
- **Liveness**: `GET /healthz` — unversioned, exempt from IP rate limiting, returns `200 OK` while HTTP listener is responsive (remains `200` during transient downstream DB/Kafka outages so orchestrators do not thrash-restart healthy pods).
- **Readiness**: `GET /readyz` — checks PostgreSQL pool ping AND Kafka broker dial within `READY_CHECK_TIMEOUT` (default `2s`). Returns `200 OK` when all dependencies are healthy or `503 Service Unavailable` (`service_not_ready`) naming the failing dependency in `error.details`.

### Correlation IDs & Structured Logs
- Every HTTP request carries `X-Request-ID` (validated `^[A-Za-z0-9._-]{8,64}$`; malformed/injected headers are replaced server-side with a fresh 32-hex ID).
- `X-Request-ID` is injected into `context.Context` and emitted on every `slog` JSON/text log line alongside redacted config (`DATABASE_URL` passwords masked) and masked phone numbers / redacted secrets.

### Profiling & Metrics Status
- **Go `pprof`**: pass `-pprof-addr=127.0.0.1:6060` (API), `:6061` (SMPP), `:6062` (Dispatcher) on loopback only (e.g. `.\scripts\dev-up.ps1 -WithPprof`). Capture via `go tool pprof http://127.0.0.1:6060/debug/pprof/heap` or `/profile?seconds=15`.
- **Prometheus `/metrics` endpoint**: **NOT IMPLEMENTED** in the current codebase; runtime telemetry relies on `/healthz`, `/readyz`, structured `slog` logs, `customer_smpp_sessions` / `message_outbox` / `routing/snapshot-status` endpoints, and on-demand loopback `pprof`.

---

## 4. Database Operations, Migrations & Backup/Restore

### Ownership & Least-Privilege Policy
- The application role `znero_app` is non-superuser (`nocreatedb`, `nocreaterole`) and has `UPDATE`/`DELETE`/`TRUNCATE` revoked on immutable tables (`audit_logs`).
- Verify table ownership before and after migrations:
  ```powershell
  powershell -File scripts/check-db-ownership.ps1 -Target test -FailOnMismatch
  ```
- If upgrading an existing database created before `BUG-046` (`system.info.view` platform-only enforcement), execute the idempotent RBAC repair script:
  ```powershell
  & 'C:\Program Files\PostgreSQL\17\bin\psql.exe' $env:DATABASE_URL -v ON_ERROR_STOP=1 -f scripts\repair-legacy-rbac-permissions.sql
  ```

### Migrations
- Schema migrations (`000001` through `000014`) live in `backend/migrations/`:
  - Apply all: `.\scripts\migrate-up.ps1`
  - Rollback *N* steps: `.\scripts\migrate-down.ps1 -Steps 1` (supports clean rollback on populated databases back through migration `1`).

### Backup & Restore Procedure
1. **Logical Custom-Format Backup**:
   ```powershell
   & 'C:\Program Files\PostgreSQL\17\bin\pg_dump.exe' $env:DATABASE_URL --no-owner --format=custom -f backup.dump
   ```
2. **Restore into Target Database**:
   ```powershell
   # Target database must be created with OWNER znero_app by DBA/postgres
   & 'C:\Program Files\PostgreSQL\17\bin\pg_restore.exe' --no-owner --role=znero_app -d $TARGET_DATABASE_URL backup.dump
   ```
3. **Post-Restore Verification**:
   - Confirm `schema_migrations` reports `14 | f` (`dirty = false`).
   - Run `check-db-ownership.ps1` to confirm 100% `znero_app` table ownership.
   - Start `znero.exe -role=api`, verify `/readyz` == `200`, and trigger `POST /api/v1/messaging/reconciliation/run` asserting `discrepancy_count == 0` and `ledger_matches == true`.

---

## 5. Common Incident Playbooks

| Symptom | Root Cause Check | Immediate Action |
| :--- | :--- | :--- |
| **`/readyz` returns `503` (`kafka`)** | Kafka KRaft broker down/restarting | Check `kafka-kraft` process / run `scripts/start-kafka.ps1`. Core HTTP/SMPP Postgres outbox delivery continues during transient broker outages; `/readyz` recovers automatically to `200`. |
| **`/readyz` returns `503` (`database`)** | PostgreSQL connection loss or `DB_MAX_CONNS` exhaustion | Verify Postgres `:5432` reachability and `pg_stat_activity` count (keep sum of `DB_MAX_CONNS` across all 6 OS roles < PostgreSQL `max_connections`). Pool auto-recovers without restart once Postgres responds. |
| **Upstream provider degradation / storm** | Provider 5xx / timeout surge | Platform Checker activates emergency kill-switch (`POST /api/v1/routing/kill-switch/disable` for connection); `dispatcher` evaluates `routingEngine.Next()` at dispatch time and fails over queued items immediately. |
| **Messages stuck in `unknown`** | Carrier timeout/disconnect after socket write | **DO NOT auto-resend** (prevents duplicates). Inspect carrier portal or wait for late DLR before `UnknownResolutionWindow`; Reaper releases hold and logs `unknown_exposure` in `/messaging/reconciliation`. |
| **Suspected session hijack / leaked refresh token** | Client replay triggers RFC 6819 family revocation | Family is automatically revoked on first replay (`401`). Platform/Org Admin can revoke active sessions or suspend user/org tree (cascade immediate revocation via Auth middleware). |

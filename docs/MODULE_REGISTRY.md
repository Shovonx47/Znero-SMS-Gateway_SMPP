# Znero International — Module Registry

This document maintains the registry of all modules in the Znero International modular monolith (`backend/internal/<module>`). Each module must maintain strict layer separation (`handler`, `service`, `repository`) and communicate across boundaries via interfaces to allow future extraction into microservices if needed.

---

## 1. Active Modules

### 1.1 `auth`
- **Location:** `backend/internal/auth`
- **Responsibilities:**
  - User authentication via Argon2id hashing, rate limiting, and account lockout.
  - Dual-token session lifecycle: 15-minute access JWT (memory only) and rotating 7-day refresh tokens (httpOnly cookie, hash in DB).
  - RFC 6819 session rotation and token reuse detection (revoking session families).
  - Two-step RFC 6238 TOTP MFA enrollment and verification with encrypted secrets and single-use recovery codes.
  - Password reset requests and confirmations with token hashing, full session revocation, and console dev mailer.
  - User invitations and acceptance flow.
  - User status management (suspend with immediate session revocation, activate).
  - Login history tracking (IP, user agent, success/failure reason).
- **Layers:**
  - `handler.go`: Public `/auth/login`, `/auth/refresh`, `/auth/logout`, `/auth/mfa/verify`, `/auth/password/*`, `/auth/invitation/*`; protected `/auth/me`, `/auth/sessions`, `/auth/mfa/*`, `/users/*`.
  - `service.go`: Business logic, password policies, session creation, challenge token hashing.
  - `repository.go`: PostgreSQL queries for `users`, `sessions`, `mfa_challenges`, `password_resets`, `user_invitations`, `login_history`, `user_recovery_codes`.
  - `middleware.go`: `Authenticate` (Bearer JWT parsing, active user/org checks, tenant context injection) and `CSRFProtection`.
  - `mailer.go`: `Mailer` interface and `DevConsoleMailer` implementation.
- **Database Tables:** `users`, `sessions`, `mfa_challenges`, `password_resets`, `user_invitations`, `login_history`, `user_recovery_codes`.

### 1.2 `org`
- **Location:** `backend/internal/org`
- **Responsibilities:**
  - Hierarchical multi-tenancy model: `platform` → `reseller` → `customer`.
  - Materialized path management (`/{root_id}/{child_id}/`) and prefix matching.
  - Organization CRUD and status transitions (active, suspended).
  - Cascade session revocation across the entire organization tree upon suspension.
- **Layers:**
  - `handler.go`: `/api/v1/organizations` with default-deny permission guards.
  - `service.go`: Hierarchy validation, path construction, audit recording.
  - `repository.go`: PostgreSQL queries for `organizations` and recursive session tree revocation.
- **Database Tables:** `organizations`.

### 1.3 `rbac`
- **Location:** `backend/internal/rbac`
- **Responsibilities:**
  - Code-defined permission catalog (`module.resource.action`).
  - System role templates (`Super Admin`, `Org Admin`, `Staff`, `Read-only`) seeded per organization.
  - Custom role management (create, update, clone, delete with usage check).
  - Strict default-deny authorization guard (`RequirePermission`).
  - Privilege escalation prevention: callers cannot grant permissions they do not possess.
- **Layers:**
  - `handler.go`: `/api/v1/permissions` and `/api/v1/roles` endpoints.
  - `service.go`: Privilege escalation check, system role protection, role validation.
  - `repository.go`: PostgreSQL queries for `permissions`, `roles`, `role_permissions`, `user_roles`.
  - `middleware.go`: `RequirePermission(code)` fiber middleware.
- **Database Tables:** `permissions`, `roles`, `role_permissions`, `user_roles`.

### 1.4 `audit`
- **Location:** `backend/internal/audit`
- **Responsibilities:**
  - Tamper-evident, append-only system activity log.
  - Database trigger strictly blocks `UPDATE`, `DELETE`, and `TRUNCATE` operations on `audit_logs`.
  - Recursive redaction of sensitive credentials (passwords, tokens, secrets, recovery codes) from before/after payloads.
  - Tenant-scoped audit log querying and diff viewer.
- **Layers:**
  - `handler.go`: `GET /api/v1/audit/logs`.
  - `service.go`: Sensitive data sanitization and log insertion.
  - `repository.go`: PostgreSQL insertion and scoped query filtering.
  - `audit.go`: `Entry`, `Filter`, `RedactValue` definitions.
- **Database Tables:** `audit_logs` (immutable).

### 1.5 `health`
- **Location:** `backend/internal/health`
- **Responsibilities:**
  - Unversioned liveness (`/healthz`) and readiness (`/readyz`) endpoints.
  - Concurrency-isolated dependency probes for PostgreSQL and Apache Kafka.
- **Database Tables:** None.

### 1.6 `system`
- **Location:** `backend/internal/system`
- **Responsibilities:**
  - Build metadata, environment status, and database schema migration verification (`GET /api/v1/system/info`).
- **Database Tables:** `schema_info`, `schema_migrations`.

### 1.7 `diagnostics`
- **Location:** `backend/internal/diagnostics`
- **Responsibilities:**
  - Development-only Kafka diagnostics tool for testing message publication and consuming.
- **Database Tables:** None.

### 1.8 `masterdata/country`
- **Location:** `backend/internal/masterdata/country`
- **Responsibilities:**
  - Authoritative ISO 3166-1/ITU-T E.164 country reference repository.
  - Numbering plan rules, minimum/maximum national number lengths, sender ID, DND, and consent policies.
  - Full version snapshotting (`countries_history`), non-destructive rollback (creates version $N+1$).
  - Safe CSV and JSON export with formula injection sanitization.
- **Database Tables:** `countries`, `countries_history`.

### 1.9 `masterdata/operator`
- **Location:** `backend/internal/masterdata/operator`
- **Responsibilities:**
  - Network operator registry with MCC and MNC mappings, brand names, and technology capabilities (2G, 3G, 4G, 5G).
  - Enforces `UNIQUE (country_id, mcc, mnc)` allowing shared MCCs across different countries.
  - Immutable version history (`operators_history`) and rollback.
  - Safe CSV/JSON export with formula injection protection.
- **Database Tables:** `operators`, `operators_history`.

### 1.10 `masterdata/prefix`
- **Location:** `backend/internal/masterdata/prefix`
- **Responsibilities:**
  - E.164 dial prefix mappings to country and operator with type classification (mobile, fixed, other) and routing priority.
  - Reactive change notifications (`OnChange` listener) triggering immediate atomic reload of in-memory prefix trie.
  - Immutable version history (`number_prefixes_history`) and rollback.
- **Database Tables:** `number_prefixes`, `number_prefixes_history`.

### 1.11 `masterdata/approval`
- **Location:** `backend/internal/masterdata/approval`
- **Responsibilities:**
  - Dual-control Maker-Checker approval queue for master data mutations submitted without override permissions.
  - Strict enforcement: maker cannot approve their own change request (`ErrMakerCannotApprove`).
  - Mandatory rejection reason recording and complete lifecycle audit.
  - Extensible Applier interface for applying approved mutations into live tables.
- **Database Tables:** `change_requests`.

### 1.12 `masterdata/importjob`
- **Location:** `backend/internal/masterdata/importjob`
- **Responsibilities:**
  - Batch upload pipeline for CSV, XLSX, and JSON files up to 50MB.
  - Two-phase execution: upload & parse -> dry-run validation & duplicate/conflict detection -> preview report -> confirmed execution.
  - Downloadable CSV error reports with anti-formula injection safeguards.
  - Partial import option (import valid rows, skip invalid rows) and full import rollback.
- **Database Tables:** `import_jobs`, `import_job_rows`.

### 1.13 `number`
- **Location:** `backend/internal/number`
- **Responsibilities:**
  - Phone number normalization and ITU-T validation using `nyaruka/phonenumbers`.
  - Always preserves raw input number untouched (`OriginalNumber`).
  - Lock-free, zero-allocation in-memory `PrefixTrie` with `atomic.Pointer` atomic reload on master data changes.
  - Longest-prefix matching resolving country, operator, and number type.
  - Deterministic transformation rule engine: `add_prefix`, `remove_prefix`, `replace_prefix`, `add_country_code`, `remove_country_code` evaluated strictly by Scope -> Priority -> Name.
  - Single and bulk resolution endpoints (batch limit: 1,000 numbers).
- **Database Tables:** `number_transform_rules`, `number_transform_rules_history`.

### 1.14 `secrets`
- **Location:** `backend/internal/secrets`
- **Responsibilities:**
  - AES-256-GCM symmetric envelope encryption with versioned key IDs (`SECRETS_ENCRYPTION_KEY`, `SECRETS_KEY_ID`, `SECRETS_PREVIOUS_KEYS`) supporting key rotation (`RotateEnvelope`).
  - Cryptographic one-time credential generation:
    - Customer HTTP API keys (`GenerateAPIKey`: `znk_live_<base64url>` with 12-char visible prefix and SHA-256 hash).
    - Customer SMPP passwords (`GenerateSMPPPassword`: 16-char alphanumeric secret hashed with Argon2id).
  - Secret metadata formatting (`SecretStatus`: `{ is_set, changed_at, masked_display: "••• set" }`).

### 1.15 `provider`
- **Location:** `backend/internal/provider`
- **Responsibilities:**
  - Platform-only upstream provider CRUD (`smpp`, `http`, `other`), connection configuration (`provider_connections`), and country/operator coverage (`provider_coverage`).
  - Write-only encrypted secrets (`smpp_password_encrypted`, `http_credentials_encrypted`) via `secrets.KeyRing`.
  - SSRF and network validation on SMPP hosts, HTTPS callback/base URLs, and CIDR allowlists (rejecting loopback, RFC 1918 private ranges, link-local/cloud metadata `169.254.0.0/16`, and CGNAT).
  - Maker-Checker integration for provider status changes, connection status activation, and coverage activation (`declared` → `tested` → `active`).
  - Full version snapshotting and rollback (`providers_history`, `provider_connections_history`, `provider_coverage_history`) free of plaintext/ciphertext secrets.
- **Database Tables:** `providers`, `providers_history`, `provider_connections`, `provider_connections_history`, `provider_coverage`, `provider_coverage_history`.

### 1.16 `customer`
- **Location:** `backend/internal/customer`
- **Responsibilities:**
  - Customer commercial profiles (`prepaid`/`postpaid`, credit limit, billing currency, payment terms, max TPS, webhook URL with SSRF protection, DLR preferences) scoped to organization hierarchy (`platform` sees all, `reseller` sees own subtree, `customer` sees own org read-only).
  - Maker-Checker workflow for commercial profile credit limit, account type, max TPS, and status changes.
  - Customer HTTP API key lifecycle (`CreateAPIKey`, `RotateAPIKey`, `RevokeAPIKey`) and Customer SMPP bind lifecycle (`CreateSMPPAccount`, `UpdateSMPPAccount`, `ResetSMPPPassword`, `RevokeSMPPAccount`), returning plaintext secrets **once** at creation/rotation.
- **Database Tables:** `customer_profiles`, `customer_profiles_history`, `customer_api_keys`, `customer_smpp_accounts`.

### 1.17 `senderid`
- **Location:** `backend/internal/senderid`
- **Responsibilities:**
  - Sender ID registry (`alphanumeric` 3–11 chars, `numeric` 3–15 digits, `shortcode` 3–8 digits) scoped to organization hierarchy and optional destination country.
  - Strict separation of submission (`pending`) and review (`ApproveSenderID` / `RejectSenderID` with mandatory reason and `Maker != Checker` enforcement).
  - Surfaces destination country `sender_id_policy_notes` from Phase 3 country reference data.
  - Full version history (`sender_ids_history`).
- **Database Tables:** `sender_ids`, `sender_ids_history`.

### 1.18 `rating`
- **Location:** `backend/internal/rating`
- **Responsibilities:**
  - Wholesale SMS rate card engine supporting `buy` and `sell` decks with effective dating (`effective_from`, `effective_to`).
  - Strict margin protection preventing rates from being published below provider cost.
  - Multi-tenant rate visibility: resellers and customers never see provider costs or buy cards.
- **Database Tables:** `rate_cards`, `rate_entries`, `rate_cards_history`.

### 1.19 `ledger`
- **Location:** `backend/internal/ledger`
- **Responsibilities:**
  - Double-entry accounting engine enforcing $\sum \text{Debits} = \sum \text{Credits}$ per transaction in fixed scale `NUMERIC(18, 4)`.
  - Append-only database immutability: triggers strictly block `UPDATE` and `DELETE` on accounts, entries, and transactions.
  - Wallet balance caching with `SELECT ... FOR UPDATE` concurrency locks preventing overspend (prepaid $\ge 0$, postpaid $\ge -\text{credit\_limit}$).
  - Holds lifecycle (two-phase reserve $\to$ capture/release/expire).
  - Dual-control Maker-Checker integration for manual top-ups, adjustments, and unfreezes.
  - Balance reconciliation service and CLI tool (`reconcile-ledger.ps1`).
- **Database Tables:** `wallets`, `ledger_accounts`, `ledger_transactions`, `ledger_entries`, `holds`.

### 1.20 `billing`
- **Location:** `backend/internal/billing`
- **Responsibilities:**
  - Billing cycles, draft invoices, and sequential gap-free issuance (`INV-YYYY-XXXXXX` and `CN-YYYY-XXXXXX`).
  - Database trigger immutability blocking `UPDATE` and `DELETE` on issued invoices and invoice lines.
  - Manual payment recording settling customer invoices and posting balanced ledger top-up transactions.
  - Printable HTML document rendering in isolated sandbox iframe.
  - CSV/JSON statement export with formula injection sanitization.
- **Database Tables:** `invoices`, `invoice_lines`, `invoice_payments`, `billing_sequences`.

### 1.21 `routing`
- **Location:** `backend/internal/routing`
- **Responsibilities:**
  - Fast, deterministic in-memory routing decision engine (`Resolve()`, `Next()`, `Simulate()`).
  - Zero database queries per routing decision via lock-free immutable snapshots (`atomic.Pointer[RoutingSnapshot]`).
  - Route plans supporting Priority (fixed rank), Least Cost (`shopspring/decimal` exact arithmetic), and Weighted Split strategies.
  - Hard eligibility checklist filtering inactive providers, inactive connections, kill switch overrides, un-covered destinations, and missing buy/sell rates.
  - Multi-tenant customer plan assignments with country-specific overrides.
  - Conditional routing rules (`block`, `force`, `avoid`, `prefer`) evaluating country, operator, prefix, sender ID type, and traffic profile.
  - Dual-control Maker-Checker dual control on plan activation/deactivation, entry changes, customer assignments, and routing rules.
  - Instant Emergency Kill Switch for provider connections with mandatory audit reason; re-enabling requires approval.
  - Explainable simulation traces exposing candidate rankings, cost economics, and detailed disqualification reasons.
  - Background periodic reload worker (60s interval) with atomic pointer swap and last-known-good fallback on DB errors.
- **Database Tables:** `route_plans`, `route_plan_history`, `route_plan_entries`, `route_plan_entry_history`, `customer_route_assignments`, `customer_route_assignment_history`, `routing_rules`, `routing_rule_history`, `provider_connection_overrides`.

### 1.22 `messaging`
- **Location:** `backend/internal/messaging`
- **Responsibilities:**
  - Message lifecycle management with monthly partitioned `messages` tables and unpartitioned lookup tables (`message_idempotency_keys`, `message_provider_lookups`).
  - Atomic ingestion pipeline reserving wallet funds (`PlaceHoldTx`) and writing to transactional outbox (`message_outbox`) in a single database transaction.
  - Outbox dispatcher polling unhandled messages with advisory locks, resolving routes via `routing.Engine`, checking sender ID regulations, and dispatching to SMPP sessions.
  - Strict submission error classification (`accepted`, `retryable_reject`, `fatal_reject`, `unknown` on network drop/timeout) with non-repeating route failover (`routing.Engine.Next()`).
  - DLR processing pipeline parsing SMPP 3.4 receipts & TLVs, candidate normalization (hex/decimal), and applying per-connection status mappings (`provider_connection_dlr_mappings`).
  - Monotonic state transitions preventing out-of-order DLR regressions; recording discrepancies in `settlement_exceptions`.
  - Atomic financial settlement in the same transaction as state updates (`CaptureTx` on delivery, `ReleaseTx` on terminal failure or expiry).
  - Messaging Reaper background worker (PostgreSQL advisory lock `8888, 1001`) sweeping stuck queued/submitting messages, expiring abandoned `unknown` states, expiring old unreceipted messages, and retrying buffered unmatched DLRs.
  - Messaging reconciliation service verifying state invariants, active holds on terminal messages, and ledger vs message revenue totals.
- **Database Tables:** `messages` (partitioned monthly), `message_outbox`, `message_events`, `message_attempts`, `message_dlrs`, `message_idempotency_keys`, `message_provider_lookups`, `provider_connection_dlr_mappings`, `provider_connection_dlr_mappings_history`, `unmatched_dlrs`, `settlement_exceptions`, `messaging_reconciliation_runs`.

### 1.23 `smpp`
- **Location:** `backend/internal/smpp`
- **Responsibilities:**
  - High-performance clean-room SMPP 3.4 protocol encoder/decoder (`internal/smpp/pdu`) supporting standard PDUs and vendor TLVs.
  - SSRF-safe outbound dialer with DNS pre-resolution blocking private, loopback, carrier-grade NAT, and AWS metadata CIDRs.
  - Strict TLS 1.2+ configuration enforcing SNI hostname verification (`InsecureSkipVerify: false`).
  - Connection manager maintaining transceiver and transmitter/receiver session pairs, sliding window flow control, and automatic exponential reconnect backoff.
  - Periodic session metrics persister (5s interval) syncing live session states, in-flight counters, TPS, and uptime to `provider_connection_sessions`.
  - Realistic mock SMSC carrier simulator (`mocksmsc`) on port 2775 with configurable latency, reject codes, and DLR delivery modes (`immediate`, `delayed`, `undeliverable`, `failed`, `none`).
- **Database Tables:** `provider_connection_sessions`.

---

## 2. Shared Packages (`pkg/`)

| Package | Path | Responsibilities |
| :--- | :--- | :--- |
| **`tenant`** | `backend/pkg/tenant` | Server-derived `tenant.Context`, path prefix matching, SQL filter helpers. |
| **`tokens`** | `backend/pkg/tokens` | HMAC-SHA256 JWT manager, opaque token generation, SHA-256 token hashing. |
| **`crypto/password`** | `backend/pkg/crypto/password` | Argon2id hashing, strength validation, constant-time verification. |
| **`crypto/encrypt`** | `backend/pkg/crypto/encrypt` | AES-256-GCM symmetric encryption for secrets at rest. |
| **`crypto/totp`** | `backend/pkg/crypto/totp` | RFC 6238 TOTP generation/verification, otpauth URI, 10 recovery codes. |
| **`httpx`** | `backend/pkg/httpx` | Standard JSON envelopes, error codes, HTTP method filters, Fiber app builder. |
| **`database`** | `backend/pkg/database` | PostgreSQL `pgxpool` connection pool manager. |
| **`kafkaclient`** | `backend/pkg/kafkaclient` | Franz-go Kafka producer and consumer wrappers. |
| **`logx`** | `backend/pkg/logx` | Slog structured logging with request ID context propagation. |

---

## 3. Future Roadmap Modules (Phase 8B & Phase 9)

| Module | Target Phase | Planned Responsibilities |
| :--- | :--- | :--- |
| **`customer_api`** | Phase 8B | External customer-facing HTTP REST API for bulk and single SMS dispatch, webhooks, and balance checks. |
| **`smpp_server`** | Phase 8B | Inbound customer SMPP 3.4 server accepting binds, submit_sm PDUs, and forwarding DLRs. |
| **`reporting`** | Phase 9 | Aggregated traffic analytics, delivery latency metrics, margin and P&L dashboards. |

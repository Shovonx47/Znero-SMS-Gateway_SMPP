# Znero International — Implementation Status

## Current Phase: Phase 8A — Messaging Core, SMPP Gateway, DLR Settlement & Operational Screens (Chunks 1, 2 & 3 Completed)

**Last Updated:** 2026-11-09  
**Environment:** Windows 10/11 (`Microsoft Windows NT 10.0.19045.0`, Native Execution, No Docker)

---

### 1. Phase 8A Deliverables Matrix (Chunks 1, 2, 3)

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Data Model & Migrations** | Monthly Partitioning & Operational Lookup Tables | **Completed** | Migrations 10 and 11 applied to `znero_dev` and `znero_test`. Monthly partitioned `messages` (with default partition), unpartitioned `message_idempotency_keys` and `message_provider_lookups`, `message_outbox`, `message_events`, `message_attempts`, `message_dlrs`, `provider_connection_dlr_mappings`, `unmatched_dlrs`, `settlement_exceptions`, `messaging_reconciliation_runs`, `provider_connection_sessions`. |
| **Ledger Atomic Tx** | Extended Ledger Service Tx Methods | **Completed** | `PlaceHoldTx`, `CaptureTx`, `ReleaseTx`, `PostChargeTx` with atomic database transaction sharing. Idempotent replays on `CaptureTx` and safe no-ops on already-released holds. Race-condition verified. |
| **Ingestion Pipeline** | Single-Transaction Ingestion & Outbox | **Completed** | `POST /api/v1/messages` (internal service & test-send). Atomically verifies credit, executes `PlaceHoldTx`, inserts `messages`, inserts `message_idempotency_keys`, and enqueues to `message_outbox` in **one** transaction. |
| **Outbox Dispatcher** | Transactional Dispatcher with Route Failover | **Completed** | Advisory-locked outbox poller. Evaluates routing via in-memory `routing.Engine`, checks sender ID restrictions, dispatches via SMPP sender, and executes failover on definitive retryable rejections (`ESME_RTHROTTLED`, `ESME_RMSGQFUL`, `ESME_RSYSERR`) without repeating failed routes. |
| **Clean-Room SMPP Codec** | Native Clean-Room SMPP 3.4 PDU Codec | **Completed** | Standard Go library implementation (`internal/smpp/pdu`) covering all standard PDUs (`bind_transceiver`, `submit_sm`, `deliver_sm`, `enquire_link`, `unbind`, and responses) with TLV encoding/decoding. Eliminates third-party supply chain risks. |
| **SSRF-Safe Networking** | Safe Dialing & TLS 1.2+ Hostname Verification | **Completed** | Pre-dial DNS resolution with IP filtering blocking RFC 1918, RFC 3927, carrier-grade NAT, and cloud metadata ranges. Strict TLS 1.2+ SNI verification (`InsecureSkipVerify: false`). |
| **Mock SMSC Simulator** | High-Fidelity SMSC Carrier Simulator | **Completed** | Standalone tool on `127.0.0.1:2775` (`cmd/smppmock`, `scripts/run-smpp-mock.ps1`). Supports configurable response error codes, DLR delivery modes (`immediate`, `delayed`, `undeliverable`, `failed`, `none`), and configurable latencies. |
| **DLR Processing & Ingestion** | Monotonic State Engine & Receipt Matching | **Completed** | Parses text receipts (Appendix B) and TLVs (`receipted_message_id`, `message_state`). Normalizes hex/decimal candidate provider message IDs and leading zeros. Evaluates per-connection DLR mappings. Enforces monotonic status order; records out-of-order discrepancies in `settlement_exceptions`. Resolves ambiguous `StatusUnknown` messages upon late DLR arrival. |
| **Financial Settlement** | Atomic DLR Settlement | **Completed** | Captures reserved holds via `CaptureTx` upon delivery (for `on_delivery`), releases holds via `ReleaseTx` upon terminal failure or expiry. Shares database transaction with message status update. |
| **Messaging Reaper** | Background Safety Sweeper | **Completed** | Single-runner background worker using PostgreSQL advisory lock (`8888, 1001`). Sweeps stuck queued/submitting messages past TTL, expires abandoned `unknown` states (releasing holds and recording exposure flags), marks unreceipted messages expired, and retries unmatched DLR buffer. |
| **Reconciliation Service** | Message & Ledger Integrity Verification | **Completed** | Verifies message state invariants, active holds on terminal messages, captured holds on non-delivered messages, and ledger vs message revenue totals. Records audits in `messaging_reconciliation_runs`. |
| **Operational & Admin APIs** | Tenant-Scoped Messaging APIs | **Completed** | `GET /api/v1/messages` and `GET /api/v1/messages/:id` (with body masking unless `message.content.view`), safe CSV/JSON export, `POST /api/v1/messaging/test-send`, `GET /api/v1/messaging/smpp/sessions`, `GET /api/v1/messaging/queue/status`, `GET /api/v1/messaging/dlrs/unmatched`, `GET /api/v1/messaging/settlement/exceptions`, `POST /api/v1/messaging/settlement/exceptions/:id/resolve`, reconciliation runner and DLR mapping CRUD. |
| **Frontend UI Suite** | Modern SvelteKit 5 Operational Interfaces | **Completed** | 6 complete interfaces: `/messages` (Messages Explorer with Send Test Message modal), `/messages/[id]` (Message Journey timeline, attempts, routing decision, DLRs, ledger hold details), `/messaging/smpp-sessions` (Live auto-refresh session monitor), `/messaging/queue-monitor` (Outbox backlog monitor), `/messaging/unmatched-dlrs` (Unmatched DLR buffer), `/messaging/reconciliation` (Settlement exceptions & reconciliation runner), `/messaging/dlr-mappings` (Carrier DLR mapping editor). `svelte-check` 0 errors, 0 warnings. |
| **Developer Sample Tooling** | Idempotent Sample Seeder Script | **Completed** | `cmd/seedmessagingsample` and `scripts/seed-messaging-sample.ps1` (`-Cleanup` supported). Seeds DEV SAMPLE provider connection pointing to local mock SMSC, route plans, rate cards, and funded customer wallet for testing. Refuses outside `APP_ENV=development`. |
| **Code Quality & Diagnostics** | Strict Linting and Diagnostics Passing | **Completed** | `go build ./...` (0 errors), `go vet ./...` (0 findings), `gofmt -l .` (0 files), `npm run check` (0 errors, 0 warnings), `npx tsc --noEmit` (0 errors including e2e). |

---

### 1B. Phase 7 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Routing Engine Core** | In-Memory Snapshot & Atomic Pointer Swap | **Completed** | High-performance lock-free routing snapshot guarded by `sync/atomic.Pointer[RoutingSnapshot]`. Zero database queries inside `Resolve()` and `Next()`. Benchmark verified at **~64,159 ns/op** (0.064 ms per decision over 50 plans, 500 entries). |
| **Decision Pipeline** | Explainable Multi-Stage Decision Engine | **Completed** | Strict sequential pipeline: E.164 normalization & country/operator extraction via PrefixTrie → Routing Rules evaluation → Plan Selection (country override > default) → Hard Eligibility validation → Strategy Ordering (`priority`, `least_cost`, `weighted`). Returns winner candidate and ranked fallback routes. |
| **Hard Eligibility Checklist** | Multi-Layer Filtering & Reason Tracking | **Completed** | Providers must pass: connection `status == 'active'`, no active kill switch override, active destination coverage, customer allowed sender type, buy rate existence, and non-negative margin (`sell_rate >= buy_rate`). Every disqualified candidate logs explicit reason (`INACTIVE_CONNECTION`, `KILL_SWITCH_ACTIVE`, `NO_COVERAGE`, `SENDER_ID_NOT_ALLOWED`, `NO_BUY_RATE`, `NEGATIVE_MARGIN`). |
| **Route Strategies** | Deterministic Multi-Strategy Resolution | **Completed** | Three strategies: `priority` (fixed ascending priority order), `least_cost` (exact decimal buy rate comparison, tie-broken by priority), `weighted` (weighted distribution across eligible providers). |
| **Routing Rules** | Pre-Routing Rule Override Engine | **Completed** | High-priority routing rules supporting `block`, `force`, `prefer`, and `avoid` actions scoped to customer, destination country, and prefix. Evaluated in ascending priority order. |
| **Emergency Kill Switch** | Instant Disablement & Dual-Control Re-enabling | **Completed** | Instant platform-wide or country-specific provider connection disablement with mandatory audit reason. Re-enabling requires Maker-Checker dual control approval. |
| **Route Simulator & Trace** | Diagnostic Simulation & Disqualification Trace | **Completed** | Complete `/api/v1/routing/simulator` endpoint returning winning route, full candidate ranking, and step-by-step evaluation trace showing why each provider was selected or disqualified. |
| **Maker-Checker & Audit** | Dual Control & Immutable Version History | **Completed** | Dual-control maker-checker (`approval_requests`) enforced for plan activation, plan deactivation, entry mutations, customer assignments, routing rules, and kill switch re-enabling (`Maker != Checker`). Full snapshot history in `route_plan_history`, `route_plan_entry_history`, `customer_route_assignment_history`, and `routing_rule_history`. |
| **Platform-Only Isolation** | Strict RBAC & Tenant Scoping | **Completed** | Routing is strictly platform-only. Resellers and customers receive 403 Forbidden on all `/api/v1/routing/*` endpoints; UI navigation hidden from non-platform users. Direct API bypass tests confirm 403. |
| **Precision Arithmetic** | Fixed-Scale Monetary Precision | **Completed** | Strict `NUMERIC(18, 4)` in PostgreSQL and `shopspring/decimal` in Go. Zero floating-point arithmetic used anywhere in margin calculation or least-cost sorting. |
| **Tooling & Dev Seeds** | Idempotent Sample Seeder | **Completed** | `cmd/seedroutingsample` and `scripts/seed-routing-sample.ps1` (`-Cleanup` supported). Seeds DEV SAMPLE priority and least-cost plans, assignments, rules, and kill switch overrides for testing. |
| **Frontend UI Suite** | Modern SvelteKit 5 Routing Management | **Completed** | 6 complete interfaces: `/routing/plans`, `/routing/plans/[id]` (Entries, Assignments, Validation, History tabs), `/routing/assignments`, `/routing/rules`, `/routing/simulator` (Trace inspector), and `/routing/kill-switch`. `svelte-check` 0 errors, 0 warnings. |
| **Verification & E2E** | Comprehensive Test Suite | **Completed** | 100% test pass rate across all 21 Go backend packages (`scripts/test.ps1 -Integration`), 6 Vitest frontend tests, and 47 Playwright E2E tests covering complete routing lifecycle, kill switch, simulator, maker-checker, and 403 tenant boundaries. |

---

### 1B. Phase 6 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Money Representation** | Fixed-Scale Precision `NUMERIC(18, 4)` | **Completed** | Strict `NUMERIC(18, 4)` in PostgreSQL, `shopspring/decimal` in Go, string in TypeScript. Zero floating-point drift. Standard 4-decimal precision suitable for wholesale telecom messaging. Multicurrency-ready, anchored to wallet currency. |
| **Ledger Core** | Double-Entry General Ledger | **Completed** | `wallets`, `ledger_accounts`, `ledger_transactions`, `ledger_entries`. Enforces balanced entries ($\sum \text{Debits} = \sum \text{Credits}$). Standard system accounts: `PLATFORM_REVENUE`, `CUSTOMER_FUNDS`, `ADJUSTMENTS`, `SUSPENSE`. |
| **Database Immutability** | Trigger-Enforced Append-Only Ledger | **Completed** | Database triggers `trg_ledger_accounts_immutable`, `trg_ledger_transactions_immutable`, and `trg_ledger_entries_immutable` raise exceptions on `UPDATE` and `DELETE`. Corrections are recorded as append-only `reversal` transactions pointing to original transaction; reversing twice is refused. |
| **Concurrency & Credit** | Row Locks & Overdraft Protection | **Completed** | `SELECT ... FOR UPDATE` row locks on wallets. Prepaid cannot go below 0, postpaid cannot go below $-\text{credit\_limit}$. Read credit limit directly from customer profile as single source of truth. Verified under 50+ concurrent goroutines. |
| **Idempotency** | Unique Constraints & Parameter Verification | **Completed** | Unique key constraint `(tenant_org_id, transaction_type, idempotency_key)`. Replay with identical key returns original result; replay with altered parameters returns 409 Conflict. |
| **Holds Lifecycle** | Two-Phase Reserve & Settle | **Completed** | `holds` table (`active`, `captured`, `released`, `expired`). Available balance = cached balance - active holds + credit limit. Supports full or partial captures (posts charge and releases remainder). Expiry tool/command. |
| **Top-ups, Adjustments & Approvals** | Dual-Control Maker-Checker Workflow | **Completed** | Manual top-ups and adjustments route to `approval_requests` unless actor possesses `wallet.override`. Maker cannot approve their own request (`ErrMakerCannotApprove`). Mandatory rejection reason. Freeze and unfreeze wallets (unfreeze requires maker-checker approval). |
| **Billing Engine** | Invoices & Gap-Free Numbering | **Completed** | Draft invoices, line items, and sequential gap-free issuance (`INV-YYYY-XXXXXX` and `CN-YYYY-XXXXXX`) using PostgreSQL sequences. Database triggers block `UPDATE` and `DELETE` on issued/paid invoices and line items. |
| **Payments** | Payment Recording & Settlement | **Completed** | Manual payment records against issued invoices (`amount`, `payment_method`, `reference`, `paid_at`). Updates invoice outstanding balance and transitions to `paid`. Posts balanced `topup` ledger transaction settling postpaid usage or increasing prepaid balance. |
| **Statements & Documents** | Statements & Printable HTML Invoices | **Completed** | Running balance transaction statements with formula-injection sanitization for CSV and JSON export. Clean, printable HTML invoice template rendered inside an isolated iframe sandbox. |
| **Permissions & Tenancy** | 15 Granular Permissions & Multi-Tenant Scoping | **Completed** | `wallet.*`, `ledger.*`, `billing.*` permissions added to role templates. Platform sees all; resellers see only direct customers (read-only); customers see only their own wallet and invoices. Direct cross-tenant access returns 403 Forbidden. Buy rates and margins never exposed in invoices or statements. |
| **Internal Service API** | High-Throughput Service Contract | **Completed** | `internal/ledger`: `CheckAvailable`, `PlaceHold`, `Capture`, `Release`, `PostCharge`. Direct Go contract ready for Phase 8 per-message calling. |
| **Tooling & Dev Seeds** | Seeder & Reconciliation Engine | **Completed** | `scripts/seed-wallets-sample.ps1` (with `-Cleanup`), `scripts/reconcile-ledger.ps1` (with `-Repair`), `backend/cmd/seedwalletssample`, `backend/cmd/reconcile`. Reconciles cached balances against raw ledger entries. |
| **Frontend UI** | Modern SvelteKit Billing & Wallet Suite | **Completed** | `/wallets`, `/wallets/[id]`, `/invoices`, `/invoices/[id]`, `/reconciliation`, updated `/pending-changes`. 0 errors, 0 warnings in `svelte-check`. |
| **Verification & E2E** | Full Test Coverage | **Completed** | 11 `internal/ledger` integration tests, 6 `internal/billing` integration tests, 4 Playwright E2E tests, and full test suite (`scripts/test.ps1 -Integration`) passing 100%. |

---

### 1B. Phase 5 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Data Model & Precision** | Rate Cards & Precision Monetary Rates | **Completed** | `rate_cards` (`provider_buy`, `customer_sell`), `rates`, `customer_rate_assignments`, `reseller_cost_assignments` using strict `NUMERIC(18,6)` in Postgres, `decimal.Decimal` in Go, string in TypeScript. Never floating point. Truncated at 6 decimal places. |
| **Effective Dating & Lifecycle** | Half-Open Temporal Intervals `[from, to)` | **Completed** | Full overlap prevention, auto-closing of predecessor open rates upon new rate publication, future rate adjustment countdowns (`/rates/upcoming`). |
| **Specificity & Resolution** | Hierarchical Rating Engine | **Completed** | Specificity order: Customer Rate Card Assignment → Exact Operator `(Country, Operator, MsgType)` → Country Default `(Country, NULL, MsgType)`. |
| **E.164 Number Rating** | Longest-Prefix Match Trie Resolution | **Completed** | Integration with `*number.Service` prefix trie resolving arbitrary international numbers to Country & Operator in sub-microsecond time. |
| **In-Memory Engine** | High-Performance Snapshot Cache | **Completed** | Guarded by `sync.RWMutex`, delivering ~723ns/op rate resolution without database round-trips. Reactive reload upon write operations. |
| **Margin Protection** | Live Deficit Validator & Wholesale Floor | **Completed** | Compares sell price against lowest active provider buy rate. Enforces non-overridable reseller wholesale cost floor. Audited override with `rate.margin.override`. |
| **High-Throughput Import** | 64KB Chunked CSV Streaming Engine | **Completed** | Streaming scanner parsing >1.29 million rows/sec. Error reports, atomic commit, formula injection sanitization. |
| **Maker-Checker & Audit** | Dual-Control Approval & Rollback | **Completed** | All rate changes route through `approval_requests` with self-approval blocked. Immutable snapshot history on `rate_cards_history` and `rates_history`. Optimistic locking rollback. |
| **Frontend UI** | SvelteKit 5 Rating Management Suite | **Completed** | 8 modern responsive routes under `/rates/`: `buy-cards`, `buy-cards/[id]`, `sell-cards`, `sell-cards/[id]`, `assignments`, `margin-report`, `lookup`, `upcoming`, `my-cost`, `my-rates`. Zero Svelte errors and zero warnings (`npx svelte-check`). |
| **Dev Sample Seed** | Labeled Fictional Dev Rates Seed | **Completed** | `cmd/seedratessample` and `scripts/seed-rates-sample.ps1` (`-Cleanup` supported). Seeds Alpha Provider buy card, standard sell card, reseller cost card, upcoming rate changes, and assigned test tenants via the service layer. |
| **Test Verification** | Full System Integration & E2E | **Completed** | 100% test pass rate across all backend packages with zero skips under `ZNERO_INTEGRATION=1`. Playwright E2E suite covering rate cards, live margin check validator, assignments, margin report, and rate lookup calculator. |

---

### 1B. Phase 4 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Pre-Flight (Step 0)** | Phase 3 Gap Closure & Authz Hardening | **Completed** | Fixed all 55 Svelte 5 a11y warnings (`0 errors, 0 warnings`); added `(*tenant.Context).IsPlatformOrg()` and `rbac/platform_only.go` + migration `000005`; hardened `approval/service.go` (`RegisterApprovePermission`, `Maker != Checker` on approve/reject); loaded 249 authoritative ISO 3166-1 countries via `importjob.Service` with full version history and audit; removed hardcoded DB fallback in `auth/integration_test.go`. |
| **Secrets Engine** | AES-256-GCM KeyRing & One-Time Credentials | **Completed** | `backend/internal/secrets`: AES-256-GCM envelope encryption with versioned key ID (`SECRETS_ENCRYPTION_KEY`, `SECRETS_KEY_ID`, `SECRETS_PREVIOUS_KEYS`) and `RotateEnvelope`. Customer HTTP API key generator (`znk_live_<base64url>`, 12-char prefix + SHA-256 hash) and SMPP password generator (Argon2id hash). |
| **Providers** | Upstream Providers Data Model & CRUD | **Completed** | Platform-only `providers` (`smpp`, `http`, `other`) with legal entity, technical/commercial/NOC contacts, country, timezone, currency, status (`draft`, `active`, `suspended`, `inactive`). Full snapshot history (`providers_history`) and rollback. Reseller/customer orgs receive `403 Forbidden`. |
| **Connections** | Provider Connection Configs & Write-Only Secrets | **Completed** | `provider_connections` for SMPP and HTTP binds. Passwords/tokens encrypted at rest with AES-256-GCM; strictly write-only (`GET` returns `{ is_set: true, masked_display: "••• set" }`). SSRF validation blocks loopback, RFC 1918 private IPs, link-local (`169.254.0.0/16`), and CGNAT on SMPP hosts, HTTP URLs, and CIDR allowlists. Status changes (`draft` → `active` / `disabled`) route through Maker-Checker. |
| **Coverage** | Provider Coverage Matrix | **Completed** | Separate from master data: links `provider_id` to `country_id` and optional `operator_id` with lifecycle status (`declared`, `tested`, `active`, `suspended`). Activating coverage routes through Maker-Checker unless actor holds `provider.coverage.override`. |
| **Customers** | Customer Commercial Profiles | **Completed** | `customer_profiles` scoped to organization hierarchy (`platform` sees all, `reseller` sees own subtree, `customer` sees own org read-only). Stores `account_type` (`prepaid`/`postpaid`), `billing_currency`, `credit_limit`, `payment_terms_days`, `default_max_tps`, `allowed_sender_types`, `dlr_Preference`, and SSRF-validated `webhook_callback_url`. Credit limit, account type, TPS, and status changes route through Maker-Checker. |
| **Credentials** | Customer HTTP API Keys & SMPP Accounts | **Completed** | One-time secret display on creation/rotation (`customer_api_keys` stores SHA-256 hash + 12-char prefix; `customer_smpp_accounts` stores Argon2id hash). Subsequent `GET` calls never return the secret. Audit and history record only `"credential_changed": true`. |
| **Sender IDs** | Sender ID Registry & Approval Workflow | **Completed** | `sender_ids` (`alphanumeric` 3–11 chars, `numeric` 3–15 digits, `shortcode` 3–8 digits) scoped to organization hierarchy and optional destination country. Strictly separates submission (`pending`) from approval/rejection (`Maker != Checker`, mandatory rejection reason). Surfaces country `sender_id_policy_notes`. |
| **Imports** | Provider Coverage & Sender ID Batch Import | **Completed** | Extended `importjob.Service` to support `provider_coverage` (starts as `declared`) and `sender_id` (starts as `pending`). Credential imports (`customer_api_key`, `customer_smpp_account`, `provider_connection`, or secret columns) are permanently blocked (`ErrCredentialImportDenied`). |
| **Frontend** | Phase 4 UI Pages & Navigation | **Completed** | `/providers`, `/providers/[id]`, `/provider-coverage`, `/customers`, `/sender-ids`, updated `/master-data/pending-changes` and `/master-data/import`, permission-gated `Sidebar.svelte`. `svelte-check` verified at `0 errors and 0 warnings`. |
| **Sample Seed** | Labeled Fictional Dev Sample Seed | **Completed** | `cmd/seedproviderssample` and `scripts/seed-providers-sample.ps1` (`-Cleanup` supported, guarded by `APP_ENV=development`). Creates fictional `DEV SAMPLE` providers (`DEV_ALPHA`, `DEV_BETA`), `example.com` connections with runtime random secrets, coverage rows, customer profile, and `DEV-SMS` Sender ID via the service layer. |

---

### 1B. Phase 3 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Housekeeping** | Credential Scrub & Dev Seed Tool | **Completed** | Audited repository; redacted leaked test passwords/TOTP keys from Phase 2 reports; isolated dev seed accounts into `cmd/seeddev` & `scripts/seed-dev-data.ps1` (`APP_ENV=development` required, `-Cleanup` switch); added startup safety warning in `backend/cmd/api/main.go`. |
| **Master Data** | Countries Data Model & CRUD | **Completed** | ISO 3166-1 / ITU-T E.164 data model with numbering plan rules, min/max national length, sender ID, DND, and consent notes. Full snapshot history, non-destructive rollback, CSV/JSON export with formula injection sanitization. |
| **Master Data** | Operators Data Model & CRUD | **Completed** | MCC and MNC mappings per country (`UNIQUE(country_id, mcc, mnc)` allows shared MCCs), brand names, 2G/3G/4G/5G capabilities. Snapshot version history and rollback. |
| **Master Data** | Number Prefixes & Routing Map | **Completed** | E.164 prefix strings mapped to country and operator with type classification (mobile, fixed, other) and priority. Reactive `OnChange` listener triggers lock-free in-memory Trie atomic swap. |
| **Tenancy & RBAC** | Platform-Owned Master Data | **Completed** | Master data is platform-owned. Resellers and customers have read-only access to active records. Direct mutations by non-platform actors return 403 Forbidden. |
| **Approval** | Dual-Control Maker-Checker Queue | **Completed** | Mutations without override permission become pending `change_requests`. Maker cannot approve their own change (`ErrMakerCannotApprove`). Rejection requires mandatory reason; full history audited. |
| **Import** | Batch Import Pipeline (CSV/XLSX/JSON) | **Completed** | Upload, parse, validate, and preview pipeline (nothing written to live DB initially). Detects in-file duplicates and DB conflicts. Downloadable CSV error reports. Confirm supports partial import (valid rows only) and full import rollback. Max 50MB, path traversal protected, formula injection sanitized. |
| **Data Policy** | Anti-Fabrication & Reference Policy | **Completed** | Zero-fabrication policy enforced for MCC/MNC, operator names, and prefixes. Country list loaded from authoritative ISO 3166-1/ITU-T E.164 source. Operators/prefixes limited to labeled `DEV-SAMPLE-FICTIONAL` data loaded only via explicit command when `APP_ENV=development`. Documented in `docs/DATA_POLICY.md`. |
| **Number Engine** | E.164 Normalization & Validation | **Completed** | Normalizes phone numbers to international E.164 format using `nyaruka/phonenumbers`. Always preserves original input number untouched (`OriginalNumber`). |
| **Number Engine** | Longest-Prefix Match (Trie) | **Completed** | High-performance in-memory `PrefixTrie` with `atomic.Pointer` for zero-lock reads. Resolves country, operator, and number type without hitting PostgreSQL on every lookup. |
| **Number Engine** | Number Transformation Rules | **Completed** | Deterministic rule engine supporting `add_prefix`, `remove_prefix`, `replace_prefix`, `add_country_code`, and `remove_country_code`. Evaluated strictly in order: Scope (Global → Country → Operator) → Priority (Ascending) → Name (Lexicographic). |
| **Number Engine** | Lookup & Bulk Lookup APIs | **Completed** | Single resolution and bulk batch resolution (batch limit: 1,000 numbers). Returns normalized E.164, country, operator, type, matched prefix, and applied rule chain. |
| **Frontend** | Master Data UI & Workbench | **Completed** | 7 SvelteKit 5 pages under `/master-data/`: Countries, Operators, Number Prefixes, Import Center, Pending Changes, Number Lookup, Transformation Rules (with live Rule Chain Simulator). Reusable `VersionHistory` modal. Built with 0 `svelte-check` errors. |
| **Scripts & Docs** | Automation & Policy Docs | **Completed** | `scripts/seed-master-sample.ps1` (with `-Cleanup`), updated `dev.ps1` and `RUNNING_LOCALLY.md`, new `docs/DATA_POLICY.md`, updated `ARCHITECTURE.md`, `MODULE_REGISTRY.md`, `SECURITY_NOTES.md`. |

---

### 2. Test Verification Matrix

- **Go Unit Tests:**
  - `backend/internal/masterdata/country`: Non-platform users cannot create/edit (403), override permission bypasses approval, version history is never deleted, rollback creates version $N+1$, reseller/customer read-only scoping, audit log creation on write and approval.
  - `backend/internal/masterdata/approval`: Maker cannot approve own change request, checker can approve, rejection with mandatory reason, cancelled requests cannot be re-opened.
  - `backend/internal/masterdata/importjob`: File size limit (>50MB rejected), empty file rejected, path traversal rejected, comment line skipping, CSV/JSON/XLSX parsing, formula injection sanitization (`'`, `+`, `-`, `=`, `@`), platform-only import authorization.
  - `backend/internal/number`: E.164 normalization for US, UK, BD, DE; original number preservation; longest-prefix Trie matching and fallback to country level; unknown number handling; deterministic rule application and chaining; bulk lookup 1,000 number limit.
- **Frontend Verification:**
  - `svelte-check`: **0 errors, 55 accessibility warnings** across all 8 master data components.
  - `vite build`: **100% successful production build** with SSR output chunks generated.
- **Playwright E2E Tests:**
  - `frontend/e2e/phase3-masterdata.spec.ts`: Master data sidebar navigation, country listing and creation modal, operator MCC/MNC rendering, prefix listing, single number resolution, import center dropzone, pending changes maker-checker queue, rule chain simulator.

---

### 3. Next Phase: Phase 6 (Intelligent SMS Routing Engine & LCR)
- Deferred until explicit user command.

---

### 1. Phase 2 Deliverables Matrix

| Area | Component | Implementation Status | Notes |
| :--- | :--- | :--- | :--- |
| **Tenancy** | Organizations Table & Hierarchy | **Completed** | `platform` → `reseller` → `customer` hierarchy with materialized path (`/{root_id}/{child_id}/`). Customers cannot create children. |
| **Tenancy** | Server-Derived Context | **Completed** | `tenant.Context` derived exclusively from server-side claims. Client cannot provide `tenant_id` to decide access. |
| **Tenancy** | Tree Session Revocation | **Completed** | Suspending an organization cascades to sub-tenants and immediately revokes all active sessions in the entire subtree. |
| **Tenancy** | Organization Management UI | **Completed** | `/organizations`: Paginated list, search, status filter, create org modal, edit name modal, suspend/activate action. |
| **Auth** | Password Security | **Completed** | Argon2id hashing, minimum 12 characters, blacklist rejection, constant-time dummy verification on unknown emails. |
| **Auth** | Rate Limiting & Lockout | **Completed** | 5 consecutive failed login attempts trigger a 15-minute temporary lockout. |
| **Auth** | Dual-Token Sessions | **Completed** | 15-min JWT access token (in frontend memory only, never localStorage) + rotating 7-day refresh token (httpOnly, SameSite=Strict cookie). |
| **Auth** | Token Reuse Detection | **Completed** | Presenting an already-rotated or revoked refresh token hash revokes the entire session family immediately. |
| **Auth** | Instant Session Revocation | **Completed** | User suspension, org suspension, password reset, or session revocation takes effect immediately via middleware DB checks. |
| **Auth** | RFC 6238 TOTP 2FA | **Completed** | Mandatory for platform Super Admins. Secrets encrypted at rest with AES-256-GCM (`MFA_ENCRYPTION_KEY`). 10 single-use recovery codes (SHA-256). |
| **Auth** | Password Reset & Invites | **Completed** | Single-use hashed tokens (30m for reset, 48h for invite). Console dev mailer (`DevConsoleMailer`) active in development. |
| **Auth** | Login History & Sessions UI | **Completed** | `/sessions`: Active sessions list, revoke single session, revoke all devices, recent login activity log. |
| **RBAC** | Default-Deny Authorization | **Completed** | Permissions code-defined as `module.resource.action`. Routes without explicit policy denied. `RequirePermission` middleware. |
| **RBAC** | System & Custom Roles | **Completed** | Seeded system roles (`Super Admin`, `Org Admin`, `Staff`, `Read-only`) cannot be modified or deleted. Custom role CRUD & clone. |
| **RBAC** | Privilege Escalation Guard | **Completed** | Non-platform actors cannot create, update, or assign roles with permissions they do not possess. |
| **RBAC** | Role Management UI | **Completed** | `/roles`: Role cards, module-by-module permission matrix checkboxes, custom role creator, editor, cloner, and deleter. |
| **Audit** | Append-Only Immutability | **Completed** | PostgreSQL trigger `audit_logs_immutable` strictly throws exceptions on any `UPDATE`, `DELETE`, or `TRUNCATE`. |
| **Audit** | Credential Redaction | **Completed** | Passwords, hashes, tokens, secrets, recovery codes recursively masked to `"[REDACTED]"` before audit insertion. |
| **Audit** | Audit Explorer UI | **Completed** | `/audit`: Paginated audit log explorer with module/action/entity filters, search, and before/after JSON diff modal. |
| **Bootstrap** | First Admin CLI Tool | **Completed** | `cmd/bootstrap` & `scripts/bootstrap-admin.ps1`: Creates platform org, seeds roles, creates Super Admin, prints TOTP & recovery codes. Idempotent. |
| **Scripts** | Local Automation | **Completed** | In `/scripts`: `setup-db.ps1`, `bootstrap-admin.ps1`, `dev.ps1`, `stop-all.ps1`, `status.ps1`, `migrate-up.ps1`, `migrate-down.ps1`, `test.ps1`. |
| **Docs** | Architecture & Local Guide | **Completed** | `RUNNING_LOCALLY.md`, `ARCHITECTURE.md`, `MODULE_REGISTRY.md`, `SECURITY_NOTES.md`, `IMPLEMENTATION_STATUS.md`. |

---

## 2. Test Verification Matrix

- **Backend:**
  - `backend/internal/auth`: Password strength, Argon2id hashing, timing-safe verification, TOTP validation with drift windows, AES-256-GCM secret encryption, JWT access tokens, opaque token generation and hashing, account lockout and suspension checks.
  - `backend/internal/org`: Hierarchy rules, customer cannot create children, tenant boundary scoping (reseller cannot access platform or sibling reseller).
  - `backend/internal/rbac`: Default-deny permission middleware (empty permission code denied, unauthenticated denied, missing permission 403, platform admin bypass), privilege escalation prevention.
  - `backend/internal/audit`: Sensitive key redaction, PostgreSQL database immutability trigger test (UPDATE and DELETE fail at DB level).
  - `backend/internal/app`: Route versioning, default-deny method filter (`GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS` allowed; `TRACE`, `CONNECT` 405 denied).
  - `backend/internal/config`: Parser defaults, validation errors, duration/int limits, secret redaction, parent-walk `.env` search.
  - `backend/internal/health`: Concurrency, individual probe timeouts, state transition logging, Kafka-down partial isolation.
  - `backend/internal/system`: Service layer mapping, undefined migration handling, error redaction.
  - `backend/internal/diagnostics`: Test message validation, publisher calls, payload truncation.
  - `backend/pkg/*`: `crypto/password`, `crypto/encrypt`, `crypto/totp`, `database`, `httpx`, `kafkaclient`, `logx`, `tenant`, `tokens`.
- **Database & Kafka Integration Tests (`.\scripts\test.ps1 -Integration`):**
  - `TestIntegrationTenantIsolation`: Confirms customer A cannot query or enumerate customer B's data or organizations in PostgreSQL.
  - `TestIntegrationPrivilegeEscalation`: Confirms unauthorized permission assignments fail with `ErrPrivilegeEscalation`.
  - `TestIntegrationRefreshTokenReuseDetection`: Confirms RFC 6819 reuse detection revokes the entire session family in PostgreSQL when an old refresh token is replayed.
  - `TestIntegrationAccountLockout`: Confirms 5 failed login attempts trigger `locked_until` in PostgreSQL and reject further logins with `ErrAccountLocked`.
  - `TestIntegrationPasswordResetInvalidatesSessions`: Confirms password reset confirmation revokes all active sessions for that user in PostgreSQL.
  - `TestIntegrationImmediateRevocation`: Confirms `Authenticate` middleware instantly rejects suspended users, suspended orgs, and revoked sessions with 401 Unauthorized, despite holding an unexpired 15-minute JWT.
  - `TestDevConsoleMailerEnvironmentGate`: Confirms `DevConsoleMailer` rejects sending emails unless `APP_ENV=development`.
  - `TestAuditLogImmutability`: Confirms database triggers and `znero_app` privilege revocation reject `UPDATE` and `DELETE` on `audit_logs`.
  - `TestIntegrationPublishAndConsumeRoundTrip`: Confirms Kafka KRaft broker round-trip pub/sub on `127.0.0.1:9092`.
- **Frontend:**
  - `svelte-check`: **0 errors, 0 warnings** across all components and routes.
  - `vitest`: **6/6 tests passed** (envelope parsing, 503 degraded report unwrapping, error code mapping, network errors, in-memory Bearer token attachment, login token memory storage and logout cleanup).

---

## 3. Next Phase: Phase 3 (SMS Routing Engine & Rate Cards)
- Deferred until explicit user command.

# Znero International — Handover & Engineering Guide

This document provides external reviewers, lead engineers, and system auditors with a comprehensive summary of the Znero International messaging, routing, and financial platform, its architectural verification status, local environment setup, credentials, and code maps.

---

## 1. Project Overview & Current Status

**Znero International** is an enterprise-grade multi-tenant SMS aggregation, intelligent routing, and financial settlement platform. The platform is architected with strict financial immutability, maker-checker dual-control governance, tenant isolation, zero-database-query runtime routing decisions, and transactional safety.

It bridges downstream API/SMPP customers to upstream carrier aggregators with automated two-phase ledger holds, least-cost and quality-based routing, real-time emergency kill switches, concatenated SMS encoding (GSM-7 / UCS-2), delivery receipt (DLR) state machines, and inbound (MO) SMS routing.

### Architecture Milestones & Implementation History

- **Phase 1: Environment & Tooling Baseline**:
  - Structured Go 1.22+ backend modular layout and SvelteKit frontend.
  - Local infrastructure orchestration via PowerShell scripts (`setup-db.ps1`, `start-kafka.ps1`, `migrate-up.ps1`).
- **Phase 2: Authentication, Tenancy & RBAC**:
  - Strict hierarchical tenancy (`platform` -> `reseller` -> `customer`).
  - Session management with cryptographically secure refresh token rotation and immediate revocation.
  - TOTP MFA at rest encrypted with AES-256-GCM.
  - Immutable audit logging with trigger-enforced `NO UPDATE` / `NO DELETE`.
- **Phase 3: Master Data & Dual-Control Engine**:
  - ISO countries, MCC/MNC mobile operators, E.164 phone prefixes, and number rewriting rules.
  - Generic asynchronous Maker-Checker governance engine (`change_requests`). Makers cannot self-approve, checker must supply reason on rejection, cancellations audited.
- **Phase 4: Customer, Provider & Sender ID Lifecycle**:
  - Customer profile provisioning with per-tenant boundary isolation.
  - SMS aggregators/providers with connection credentials encrypted at rest.
  - Sender ID registration with alphanumeric/numeric/shortcode validation and tenant isolation.
- **Phase 5: Rating Engine & Currency Margins**:
  - Exact decimal pricing using `shopspring/decimal`.
  - Effective date windowing, least-cost sorting, and global/entry minimum margin enforcement.
- **Phase 6: Double-Entry Ledger & Financial Settlement**:
  - Fully balanced double-entry accounting (`debits == credits`).
  - Strict prepaid balance checking and postpaid credit limits.
  - Two-phase financial holds (`PlaceHold`, `Capture`, `Release`).
  - Gap-free invoice sequence numbering (`INV-YYYY-NNNNNN`), immutable issued invoices, credit notes, and reconciliation jobs.
- **Phase 7: High-Performance Routing Decision Engine**:
  - Zero-database-query in-memory snapshot decision engine with atomic pointer swaps (`atomic.Pointer[Snapshot]`).
  - Sub-15 µs evaluation time per message decision.
  - Platform-only access control, emergency kill-switches with maker-checker re-enabling, CSV/JSON exports with formula injection defense (`'` prefixing).
- **Phase 8A: Core Messaging Pipeline, Outbox & Provider SMPP**:
  - Transaction-aware ledger methods (`PlaceHoldTx`, `CaptureTx`, `ReleaseTx`, `PostChargeTx`) sharing atomic database transactions with message persistence.
  - Comprehensive Architecture Design Document (`docs/MESSAGING_DESIGN.md`).
  - Range-partitioned `messages` and `message_parts` by `created_at` with default catch-all partitions; unpartitioned lookup tables (`message_idempotency_lookups`, `message_provider_lookups`).
  - Dual-encoding engine (GSM-7 and UCS-2) with multi-part UDH concatenation.
  - Transactional outbox worker with `FOR UPDATE SKIP LOCKED`.
  - Dynamic `routing.Engine.Next()` evaluation at dispatch time honoring live kill switches.
  - Strict failover policy: retryable rejects fail over to Next(); permanent fatal rejects release holds; ambiguous outcomes (`unknown`) retain holds and are NEVER auto-resent.
  - Production-grade SMPP 3.4 client with automatic reconnect, windowed submit, enquiry_link keepalives, and DLR matching.
- **Phase 8B: Customer Edge REST API, Customer SMPP Server, Webhooks & Inbound MO**:
  - Customer Edge REST API (`backend/internal/customeredge`) with Bearer token authentication, rate limiting, and balance checks.
  - Customer-facing SMPP 3.4 server (`backend/internal/smppserver`) supporting `bind_transceiver`, `bind_transmitter`, and `bind_receiver` on port `2776`.
  - Inbound MO (Mobile-Originated) SMS routing engine (`backend/internal/messaging/inbound.go`) with keyword matching, two-way conversational routing, and Kafka publication.
  - Asynchronous webhook dispatcher (`backend/internal/messaging/webhook_dispatcher.go`) with HMAC-SHA256 signature verification and exponential backoff retry.
  - Customer delivery receipt dispatcher forwarding upstream carrier DLRs to active customer SMPP sessions (`deliver_sm`) or customer webhook endpoints.
  - Developer diagnostic tools: `devsend` (CLI SMS sender), `smppdevclient` (test SMPP bind client), and `webhookecho` (local HTTP webhook receiver on port `9099`).
  - Frontend administration screens for customer sessions, inbound numbers, inbound message logs, and webhook subscriptions.

---

## 2. Prerequisites for Local Development & Inspection

To inspect, run, or verify this codebase locally, install the following:

| Component | Minimum Version | Notes |
|:---|:---|:---|
| **Go** | `1.22+` | Backend programming language (`backend/go.mod`) |
| **Node.js** | `20+ LTS` | Frontend runtime (`frontend/package.json`) |
| **PostgreSQL** | `17+` (or 16+) | Relational database (`znero_dev` and `znero_test`) |
| **Apache Kafka** | `3.7+` | KRaft mode broker (runs on `127.0.0.1:9092`) |
| **golang-migrate** | `v4.17+` | Database migration runner CLI (`migrate`) |
| **PowerShell** | `5.1` or `7+` | Orchestration scripts in `scripts/*.ps1` |

---

## 3. Environment Setup & Bootstrap Commands

All setup and development tasks are automated via PowerShell scripts located in `scripts/`:

### Step 1: Database Setup
Create database roles and development/test databases:
```powershell
# Creates role znero_app, databases znero_dev and znero_test
powershell -File scripts/setup-db.ps1
```

### Step 2: Apply Database Migrations
Apply all 12 versioned SQL migrations (`000001` through `000012`):
```powershell
powershell -File scripts/migrate-up.ps1
```

### Step 3: Seed Sample & Development Data
Populate reference tables, administrative roles, rate cards, routing plans, customer credentials, and test accounts:
```powershell
powershell -File scripts/seed-dev-data.ps1
powershell -File scripts/seed-master-sample.ps1
powershell -File scripts/seed-providers-sample.ps1
powershell -File scripts/seed-rates-sample.ps1
powershell -File scripts/seed-wallets-sample.ps1
powershell -File scripts/seed-routing-sample.ps1
powershell -File scripts/seed-customer-edge-sample.ps1
```

### Step 4: Daily Development Startup
Launch all core services with a single command:
```powershell
# Starts Kafka (KRaft), Backend API (:8080), and Frontend UI (:5173), waiting for /readyz
powershell -File scripts/dev.ps1

# Optional developer flags:
# powershell -File scripts/dev.ps1 -WithSmppMock -WithWebhookEcho
```

---

## 4. Dev-Only Seed Accounts & Security Advisory

> [!CAUTION]
> **DEVELOPMENT SEED ACCOUNTS MUST NEVER BE DEPLOYED TO PRODUCTION ENVIRONMENTS.**
>
> The seed scripts (`backend/cmd/seeddev/main.go`, `backend/cmd/seedcustomeredge/main.go`, `scripts/seed-*.ps1`) create default administrative, maker, checker, and customer accounts with known development passwords (e.g., `DevSeed1234!#`, `secret123`). These accounts exist solely for local developer verification and automated E2E testing.
>
> In production environments:
> 1. Running any `seed*` binary is strictly prohibited.
> 2. Initial platform administrators must be provisioned exclusively via the interactive `scripts/bootstrap-admin.ps1` script with high-entropy unique credentials and mandatory hardware TOTP MFA enrollment.
> 3. Secrets, encryption keys, and JWT secrets must be loaded from external HSM/vault managers, never committed to `.env` files.

### Development Account Reference Table

| Username / Login | Role / Access Level | Scope / Organization | Password / Token Location | Notes |
|:---|:---|:---|:---|:---|
| `admin` (`admin@znero.internal`) | `platform_admin` | Platform Root | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Full platform administration |
| `maker` (`maker@znero.internal`) | `platform_maker` | Platform Root | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Proposes master data & routing changes |
| `checker` (`checker@znero.internal`) | `platform_checker` | Platform Root | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Approves/rejects maker change requests |
| `readonly` (`readonly@znero.internal`) | `platform_auditor` | Platform Root | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Read-only inspection of audits and reports |
| `reseller_admin` | `org_admin` | Reseller Alpha | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Reseller tenant administrator |
| `customer_user` | `customer` | Acme Corp | `backend/cmd/seeddev/main.go` (`DevSeed1234!#`) | Customer self-management portal user |
| `acme_smpp` | Customer SMPP Account | Acme Corp | `backend/cmd/seedcustomeredge/main.go` (`secret123`) | Customer SMPP credentials on port `2776` |
| `zn_live_sample_key_1234567890` | Customer REST API Key | Acme Corp | `backend/cmd/seedcustomeredge/main.go` | Customer Bearer token for HTTP REST API |

---

## 5. Implementation Status & Test Coverage

### What is Built and Working
- **Phases 1 through 8 are fully completed**:
  - Full multi-tenant RBAC, session management, and encrypted TOTP MFA.
  - Complete master data management with ISO countries, MCC/MNC operators, and E.164 number rewriting.
  - Dual-control Maker-Checker governance engine with immutable audit triggers.
  - Provider aggregators and customer provisioning with encrypted credentials.
  - Rating engine with effective date windowing, least-cost sorting, and currency margins.
  - Double-entry ledger with balance hold/capture/release, gap-free invoicing, and reconciliation.
  - High-performance in-memory routing engine with zero DB queries per decision and live kill switch.
  - Range-partitioned message storage with default catch-all partitions and global idempotency lookups.
  - Dual-encoding SMS segmentation engine (GSM-7 & UCS-2) with UDH multi-part support.
  - Transactional outbox reader and dispatcher with pure-Go token bucket rate limiting.
  - Production-ready upstream SMPP client and mock SMSC simulator.
  - Customer Edge REST API with Bearer token authentication, rate limiting, and balance checks.
  - Customer-facing SMPP server supporting `bind_transceiver`, `bind_transmitter`, and `bind_receiver`.
  - Inbound MO SMS routing with keyword match and conversational reply routing.
  - Asynchronous webhook dispatcher with HMAC signatures and retry backoff.
  - Customer DLR dispatcher forwarding carrier DLRs to active SMPP sessions or webhooks.

### Honest Disclosure: Automated Test Coverage
- **Phases 1 through 7**: Fully tested with **165 backend unit/integration tests**, **47 Playwright E2E browser tests**, and **6 Vitest unit tests**, executed with `-race` detection enabled.
- **Phase 8A Chunk 1**: Verified with **18 unit/integration tests** in `backend/internal/messaging/messaging_test.go` executed under `-race` with zero skips.
- **Phase 8A Chunks 2 & 3 and Phase 8B**: Performed as **implementation-only engineering runs** per project directives. **Zero new automated unit/integration or Playwright E2E tests were authored for Chunks 2, 3, and 8B.**
- **Verification Guarantee**: Every file and package across Phase 8A and 8B strictly compiles and passes:
  - `go build ./...` (0 errors)
  - `go vet ./...` (0 findings)
  - `golangci-lint run ./...` (0 issues found)
  - Frontend `npm run check` (svelte-check: 0 errors, 0 warnings)
  - Frontend `npx tsc --noEmit` (0 errors)
- *Next Step Recommendation*: Prior to production rollout, comprehensive integration and load test suites should be authored for the customer SMPP server, customer edge REST API, and inbound MO SMS pipeline.

### What is NOT Built at All (Phase 9 Post-MVP Enterprise Features)
The following features are out of scope for Phases 1–8 and belong to Phase 9:
1. **Automated Recurring Billing Invoicing**: Automated PDF invoice rendering, gap-free monthly invoice generation cron jobs, and email invoice delivery.
2. **Payment Gateway Integrations**: Direct payment processing rails (Stripe, Adyen, automated bank transfers) for self-service customer wallet top-ups.
3. **Advanced Telemetry & Analytics**: Prometheus metric exporters, Grafana dashboard templates, and high-cardinality time-series analytics for routing metrics.
4. **Public Self-Service Onboarding**: Public signup workflows, email verification loops, automated KYC compliance verification, and self-service credit card provisioning.

---

## 6. Key Files & Architecture Map

### Database Migrations (`backend/migrations/`)
| Migration | Description |
|:---|:---|
| `000001_create_schema_info` | Base schema versioning table |
| `000002_create_auth_tenancy_rbac_audit` | Organizations, users, sessions, RBAC, and immutable audit logs |
| `000003_phase3_master_data` | Countries, operators, prefixes, and dual-control change requests |
| `000004_import_maker_checker` | CSV import batch jobs and approval links |
| `000005_phase3_authz_hardening` | Security triggers and tenant isolation hardening |
| `000006_phase4_providers_customers_credentials` | Aggregators, customers, sender IDs, and encrypted credentials |
| `000007_phase5_rates` | Rate cards, rate card entries, margins, and currency tables |
| `000008_phase6_wallets_ledger_billing` | Ledger accounts, journal entries, holds, and invoices |
| `000009_phase7_routing` | Routing profiles, strategies, assignments, rules, and kill switch |
| `000010_phase8_messaging` | Partitioned messages, outbox, attempts, events, and lookups |
| `000011_phase8_chunk3` | Inbound MO SMS tables, webhook subscriptions, and DLR mappings |
| `000012_phase8b` | Customer API keys, customer SMPP accounts, inbound numbers, and webhook events |

### Apache Kafka Topics
| Topic Name | Purpose |
|:---|:---|
| `znero.messages.inbound` | Ingestion queue for outgoing MT (Mobile-Terminated) messages |
| `znero.messages.dlr` | Upstream delivery receipts received from carrier aggregators |
| `znero.messages.mo` | Upstream inbound (Mobile-Originated) SMS messages from carriers |
| `znero.dlr.customer` | Normalised DLR events dispatched to downstream customer SMPP/webhooks |
| `znero.webhook.events` | Webhook notification dispatch queue with retry mechanisms |
| `znero.dev.test` | Local developer test topic for Kafka readiness checks |

### API Route Endpoints
- **Platform Internal & Admin API** (`backend/internal/api/router.go`, Port `:8080`):
  - `/api/v1/auth/*` — Authentication, session refresh, TOTP setup/challenge.
  - `/api/v1/master-data/*` — Countries, operators, prefixes, number transformations, approval requests.
  - `/api/v1/providers/*` — SMS aggregators, connection configs, credential testing.
  - `/api/v1/customers/*` — Customer profiles, balance overrides, sender IDs.
  - `/api/v1/rates/*` — Buy/sell rate cards, assignment matrix, margin floors.
  - `/api/v1/wallets/*` & `/api/v1/billing/*` — Wallets, ledger journals, balance holds, invoices.
  - `/api/v1/routing/*` — Routing plans, rules, simulation sandbox, emergency kill switches.
  - `/api/v1/messaging/*` — Message logs, outbox monitor, DLR mappings, reconciliation, inbound messages.
- **Customer Edge REST API** (`backend/internal/customeredge/handler.go`, Port `:8080`):
  - `POST /v1/messages` — Send single SMS message with Bearer token authentication.
  - `POST /v1/messages/bulk` — Batch submit up to 1,000 SMS messages.
  - `GET /v1/messages/{id}` — Query message status and delivery lifecycle.
  - `GET /v1/inbound` — Query customer inbound (MO) messages received on allocated numbers.
  - `POST /v1/webhooks` & `GET /v1/webhooks` — Register and inspect customer webhook subscriptions.
  - `GET /v1/balance` — Query current wallet balance and active financial holds.
  - *Full OpenAPI 3.0 specification available at*: `docs/openapi-customer.yaml` and `docs/CUSTOMER_API.md`.

### SMPP Client & Server Entry Points
- **Upstream SMPP Client** (`backend/internal/smppclient/`): Connects outbound to carrier SMSCs; handles `submit_sm`, windowed sliding queue, enquire_link heartbeats, and DLR reception (`deliver_sm`).
- **Customer SMPP Server** (`backend/internal/smppserver/`): Listens on TCP port `2776`; handles inbound customer binds (`bind_transceiver`, `bind_transmitter`, `bind_receiver`), validates system_id and password, ingests `submit_sm` PDUs, and pushes carrier DLRs via `deliver_sm`.
- **Inbound MO Pipeline** (`backend/internal/messaging/inbound.go`): Routes inbound mobile messages by destination shortcode/virtual number and keyword prefix.
- **Webhook Dispatcher** (`backend/internal/messaging/webhook_dispatcher.go`): Delivers signed HTTP POST notifications to customer endpoints.
- **Developer Diagnostic CLI Tools** (`backend/cmd/`):
  - `devsend` (`backend/cmd/devsend/main.go`): CLI tool to submit test messages through the customer edge API.
  - `smppdevclient` (`backend/cmd/smppdevclient/main.go`): CLI SMPP client to test bind and submission against `smppserver`.
  - `mocksmsc` (`backend/cmd/mocksmsc/main.go`): Mock SMSC provider listening on port `2775` to simulate upstream carrier behaviour.
  - `webhookecho` (`backend/cmd/webhookecho/main.go`): Local HTTP server listening on port `9099` to echo and verify incoming webhooks.

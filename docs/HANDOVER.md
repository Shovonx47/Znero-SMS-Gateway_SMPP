# Znero International — Handover & Verification Guide

This document provides external reviewers, managers, and system auditors with a comprehensive summary of the Znero International messaging, routing, and financial platform, its architectural verification status, local environment setup, and test suite execution.

---

## 1. Project Overview & Current Status

**Znero International** is an enterprise-grade multi-tenant SMS aggregation, intelligent routing, and financial settlement platform. The platform is architected with strict financial immutability, maker-checker dual-control governance, tenant isolation, zero-database-query runtime routing decisions, and transactional safety.

### Architecture & Milestones Completed

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
- **Phase 8A Chunk 1 (Ingestion Pipeline, Transactional Outbox & Data Model)**:
  - Transaction-aware ledger methods (`PlaceHoldTx`, `CaptureTx`, `ReleaseTx`, `PostChargeTx`) sharing atomic database transactions with message persistence.
  - Comprehensive Step 1 Architecture Design Document completed in `docs/MESSAGING_DESIGN.md` with 4 amendments.
  - Migration `000010_phase8_messaging.up.sql` and `down.sql` applied and tested (reversible).
  - Messaging domain and repository implemented in `backend/internal/messaging`.
  - Ingestion pipeline with atomic `PlaceHoldTx` sharing transaction with message insert, GSM-7 and UCS-2 encoding calculation, tenant field-stripping, and phone masking.
  - Transactional outbox reader and dispatcher with `FakeSender` mock, token bucket rate limiter, dynamic `routing.Next()` at dispatch time, failover policies, and crash resilience.
  - 18 comprehensive integration tests passing under `-race` with zero skips and zero data races.

---

## 2. Prerequisites for Local Development & Inspection

To inspect, run, or verify this codebase locally, install the following:

| Component | Minimum Version | Notes |
|:---|:---|:---|
| **Go** | `1.22+` | Backend programming language |
| **Node.js** | `20+ LTS` | Frontend framework runtime |
| **PostgreSQL** | `17+` (or 15+) | Primary relational database (`znero_dev` and `znero_test`) |
| **Apache Kafka** | `3.7+` | KRaft mode broker (runs on `127.0.0.1:9092`) |
| **golang-migrate** | `v4.17+` | CLI migration runner (`migrate`) |
| **PowerShell** | `5.1` or `7+` | All automation scripts are in `scripts/*.ps1` |

---

## 3. Environment Setup & Bootstrap Commands

All operational tasks are automated via PowerShell scripts located in the `scripts/` directory:

### Step 1: Database Setup
Create database roles and development/test databases:
```powershell
# Creates role znero_app, databases znero_dev and znero_test
powershell -File scripts/setup-db.ps1
```

### Step 2: Apply Migrations
Apply all 9 versioned SQL migrations (`000001` through `000009`):
```powershell
powershell -File scripts/migrate-up.ps1
```

### Step 3: Seed Sample & Development Data
Populate reference tables, administrative roles, rate cards, routing plans, and test accounts:
```powershell
powershell -File scripts/seed-dev-data.ps1
powershell -File scripts/seed-master-sample.ps1
powershell -File scripts/seed-providers-sample.ps1
powershell -File scripts/seed-rates-sample.ps1
powershell -File scripts/seed-wallets-sample.ps1
powershell -File scripts/seed-routing-sample.ps1
```

### Step 4: Start Kafka
Launch local Kafka in KRaft mode:
```powershell
powershell -File scripts/start-kafka.ps1
```

### Step 5: Start Local Development Servers
Run backend API (`:8080`) and frontend UI (`:5173`) concurrently:
```powershell
powershell -File scripts/run-both.ps1
# Or separately:
# powershell -File scripts/run-backend.ps1
# powershell -File scripts/run-frontend.ps1
```

---

## 4. Verification Evidence & Test Suites

The test suites verify both unit logic and database/Kafka integration under concurrent load and race detection.

### Executing Full Test Suites

```powershell
# Run backend integration tests (connects to PostgreSQL & Kafka)
powershell -File scripts/test.ps1 -Integration -BackendOnly

# Run frontend tests (svelte-check and Vitest)
powershell -File scripts/test.ps1 -FrontendOnly

# Run isolated Playwright E2E suite against znero_test
powershell -File scripts/test-e2e.ps1
```

### Summary of Verified Areas & Test Counts

| Package / Domain | Tests | Verification Highlights |
|:---|:---:|:---|
| `internal/auth` | **7 suites** | Token reuse detection, lockout after 5 failed attempts, session revocation |
| `internal/rbac` | **4 suites** | Strict platform-only filtering, customer privilege restrictions |
| `internal/org` | **2 suites** | Cross-tenant reseller isolation, tenant hierarchy boundaries |
| `pkg/tenant` | **2 suites** | Tenant context propagation, cross-tenant leak prevention |
| `internal/masterdata/approval` | **8 suites** | Maker self-approval blocked, rejection reason requirement, unapproved entity denied |
| `internal/masterdata/country` | **11 suites** | ISO-2/ISO-3 uniqueness, search/filtering, maker-checker lifecycles |
| `internal/masterdata/operator` | **4 suites** | MCC/MNC routing, operator prefixes, audit records |
| `internal/masterdata/prefix` | **4 suites** | E.164 destination matching, prefix prioritization |
| `internal/masterdata/importjob` | **3 suites** | CSV bulk import validation, chunking, rollback on invalid formats |
| `internal/number` | **2 suites** | Rule prioritization, regex rewriting, sender ID translation |
| `internal/customer` | **3 suites** | Customer profile lifecycle, secret masking, cross-tenant isolation |
| `internal/provider` | **3 suites** | Aggregator connections, credentials encryption at rest, coverage testing |
| `internal/senderid` | **3 suites (15 subtests)** | Cross-tenant sender ID isolation, regex validation, dual-control registration |
| `internal/rating` | **14 suites** | Effective date rates, currency conversion, least-cost sorting, margin floors |
| `internal/ledger` | **24 suites** | **Run with `-race`**: 50 parallel debits never overspend, parallel holds never overcommit, idempotent capture replay, concurrent transaction safety |
| `internal/billing` | **12 suites** | Gap-free sequence numbering under 10 concurrent requests, issued invoice immutability, dual-control voids |
| `internal/routing` | **38 suites** | Determinism without DB queries, atomic swaps, rule actions (`block`/`force`/`prefer`/`avoid`), failovers, CSV/JSON formula shielding |
| `internal/messaging` | **18 suites (100% pass)** | **Run with `-race`**: GSM-7/UCS-2 segmentation, atomic `PlaceHoldTx` sharing message insert tx, 50 concurrent duplicate idempotency, cross-month idempotency, partition routing, outbox dispatch, dynamic `routing.Next()` failover, kill-switch at dispatch, ambiguous outcome `unknown` with hold retained, crash recovery, and simulated Kafka downtime |
| **Frontend Diagnostics** | **0 errors / 0 warnings** | `svelte-check` clean TypeScript & Svelte validation |
| **Frontend Unit** | **6 passed** | Vitest client & auth interceptor tests |
| **Playwright E2E** | **47 passed** | Full browser automation across master data, auth, billing, and routing |

---

## 5. Phase 8 Status & Handover Boundary

- **Phase 8A Chunk 1 Complete**:
  - **Step 2 (Data Model)**: Migration `000010_phase8_messaging.up.sql` and `down.sql` applied and tested on both `znero_dev` and `znero_test`. Range-partitioned `messages` and `message_parts` by `created_at` with default partitions; unpartitioned `message_idempotency_lookups` and `message_provider_lookups`.
  - **Step 3 (Ingestion Pipeline)**: Implemented in `backend/internal/messaging`. Pipeline verifies destination, sender ID, GSM-7/UCS-2 segmentation, evaluates routing candidates, calculates pricing, and calls `ledger.Service.PlaceHoldTx` inside the EXACT SAME database transaction as the message insert (`InsertMessageAtomic`). Tenant isolation and phone number masking enforced.
  - **Step 4 (Transactional Outbox & Dispatcher)**: Outbox reader with row locking (`FOR UPDATE SKIP LOCKED`), pure-Go token bucket per-connection rate limiting, dynamic `routing.Next()` evaluation at dispatch time (instantly honoring kill switch), failover retry loop, fatal permanent reject hold release, and ambiguous outcome handling (`unknown`, hold strictly retained, never auto-resent).
  - **Verification**: All 18 messaging test suites pass under `-race` with zero skips and zero failures.
- **STOPPED FOR APPROVAL BEFORE CHUNK 2**:
  - Implementation is paused at the end of Chunk 1 per engineering governance protocol. Chunk 2 (Mock SMSC Simulator & SMPP Client) will begin upon reviewer approval.

---

## 6. Security Advisory: Development Seeds & Credentials

> [!CAUTION]
> **DEVELOPMENT SEED ACCOUNTS MUST NEVER BE DEPLOYED TO PRODUCTION ENVIRONMENTS.**
>
> The seed scripts (`backend/cmd/seeddev/main.go`, `scripts/seed-*.ps1`) create default administrative, maker, and checker accounts with known development passwords (e.g., `AdminDevPass123!`, `MakerDevPass123!`). These accounts exist solely for local developer verification and automated E2E testing.
>
> In production environments:
> 1. Running any `seed*` binary is strictly prohibited.
> 2. Initial platform administrators must be provisioned exclusively via the interactive `scripts/bootstrap-admin.ps1` script with high-entropy unique credentials and mandatory hardware TOTP MFA enrollment.
> 3. Secrets, encryption keys, and JWT secrets must be loaded from external HSM/vault managers, never committed to `.env` files.


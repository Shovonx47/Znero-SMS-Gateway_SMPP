# Znero International — System Architecture

**Project:** Znero International — Wholesale SMS Switching Platform  
**Architecture:** Modular Monolith in Go with SvelteKit Frontend  
**Environment:** Greenfield, Local Native Development (No Docker)  
**Primary Platform:** Windows 10/11 with Cross-Platform Support (Linux-ready)

---

## 1. High-Level System Architecture

Znero International is architected as a high-throughput, low-latency wholesale SMS switching platform. The core is designed as a **Modular Monolith** in Go, where individual engines (Routing, Rates, Ledger, Audit, SMPP connectors) reside in isolated packages under `internal/<module>` with strictly defined interfaces. This architecture allows immediate co-location and transactional consistency during local greenfield development while enabling trivial extraction of high-scale engines into independent microservices in later phases.

```
+-------------------------------------------------------------------------+
|                              Client Layer                               |
|              SvelteKit 2 + TypeScript + Tailwind CSS (Vite)             |
|              (Single-Page App, Runes Reactivity, In-Memory JWT)         |
+------------------------------------+------------------------------------+
                                     | HTTP / WebSocket
                                     v
+-------------------------------------------------------------------------+
|                        Go + Fiber Modular Monolith                      |
|                                                                         |
|  [ Middleware Pipeline ]                                                |
|  - Request ID (X-Request-ID Header + Context Propagation)               |
|  - Slog Structured Access Logging (JSON in prod, text in dev)           |
|  - Panic Recovery -> Standard Error Envelope                            |
|  - Security Headers (CSP, Frame-Options, nosniff, Permissions)          |
|  - Default-Deny HTTP Method Filtering (GET, HEAD, POST, PUT, PATCH,    |
|    DELETE, OPTIONS only; TRACE, CONNECT denied)                         |
|  - IP-based Rate Limiter (with bypass for /healthz & /readyz)           |
|  - Authenticate Middleware (JWT verify, active status check)            |
|  - Default-Deny RBAC Guard (RequirePermission)                          |
|  - CSRF Origin Verification                                             |
|                                                                         |
|  [ Modules (internal/<module>) ]                                        |
|  +--------------------+  +--------------------+  +-------------------+  |
|  |       Health       |  |       System       |  |    Diagnostics    |  |
|  +--------------------+  +--------------------+  +-------------------+  |
|  |   Authentication   |  |   Organizations    |  |       RBAC        |  |
|  | - Argon2id Hashing |  | - Materialized Path|  | - System Roles    |  |
|  | - Rotating Refresh |  | - Hierarchy Scoping|  | - Custom Roles    |  |
|  | - RFC 6238 TOTP    |  | - Tree Revocation  |  | - Privilege Guard |  |
|  +--------------------+  +--------------------+  +-------------------+  |
|  |     Audit Trail    |                                                 |
|  | - Immutability Trg |                                                 |
|  | - Sensitive Redact |                                                 |
|  +--------------------+                                                 |
|                                                                         |
|  [ Shared Layer (pkg/) ]                                                |
|  - httpx (Envelopes, Error Codes)    - kafkaclient (franz-go wrapper)   |
|  - database (pgxpool connection)     - logx (slog context handler)      |
|  - crypto/password (Argon2id)        - crypto/encrypt (AES-256-GCM)     |
|  - crypto/totp (RFC 6238 TOTP)       - tokens (JWT + Opaque Hashes)     |
|  - tenant (Server-Derived Context)                                      |
+-------------------+--------------------------------+--------------------+
                    |                                |
                    v                                v
+------------------------------------+  +---------------------------------+
|       PostgreSQL 17 Database       |  |    Apache Kafka (KRaft Mode)    |
| - Connection Pool (pgx/v5)         |  | - Standalone (No ZooKeeper)     |
| - Dedicated User: znero_app        |  | - Broker: localhost:9092        |
| - Schema Migrations (golang-migrate)| | - Controller: localhost:9093    |
| - Audit Immutability Trigger       |  |                                 |
+------------------------------------+  +---------------------------------+
```

---

## 2. Organizations & Multi-Tenancy Architecture

### 2.1 Materialized Path Hierarchy
Organizations model the wholesale telecom hierarchy:
- **`platform`** (Root): Owns the switching platform, global routes, system templates.
- **`reseller`**: Buys wholesale capacity from platform, manages sub-customers and custom margins.
- **`customer`**: Leaf organization; terminates SMS traffic and consumes rate sheets. Customers cannot create sub-organizations.

Hierarchy is stored as a **materialized path** (`/{root_id}/{child_id}/`):
```sql
organizations (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL CHECK (type IN ('platform', 'reseller', 'customer')),
    parent_id UUID REFERENCES organizations(id),
    path VARCHAR(1024) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 2.2 Server-Derived Tenant Context
**Critical Rule:** The client never specifies `tenant_id` to determine access rights.
Identity and tenant boundaries are extracted exclusively from verified server-side claims:
```go
type Context struct {
    ActorUserID     uuid.UUID
    ActorOrgID      uuid.UUID
    ActorOrgType    string
    ActorOrgPath    string // e.g. "/root-uuid/reseller-uuid/"
    IsPlatformAdmin bool
    Permissions     map[string]bool
}
```
Queries filter using materialized path prefix matching:
- A platform admin accesses all records.
- A reseller or customer accesses only rows matching `path LIKE actor_path || '%'`.

### 2.3 Row-Level Security (RLS) vs Application Scoping Evaluation
- **Decision:** Multi-tenancy scoping is enforced systematically in the Go data-access layer via `tenant.Context` and `OrgPathFilter()`.
- **Reasoning:** Application-level scoping was chosen for simplicity, code transparency, and explicit query control across all repository methods. While PostgreSQL Row-Level Security (RLS) with `SET LOCAL app.current_tenant_id` is transaction-scoped and safe with connection pooling, enforcing scoping directly in the Go repository layer ensures that tenancy boundaries are visible in code, compile-time verified, easy to trace in query logs, and simple to unit test without database transaction hooks. RLS remains an optional defense-in-depth layer that can be layered on later.

### 2.4 Tree Session Revocation
When an organization is suspended:
1. Its status and all descendant organizations' status are updated to `suspended`.
2. All active user sessions belonging to that organization or any descendant organization are revoked immediately via `RevokeSessionsForOrgTree`.

---

## 3. Authentication & Session Security

### 3.1 Passwords
- **Algorithm:** Argon2id (memory: 64 MB, iterations: 3, parallelism: 2, salt: 16 bytes random).
- **Policy:** Minimum 12 characters. Reject common passwords via blacklist.
- **Timing Attack Defense:** Constant-time verification with dummy hash evaluation when an email is not found, defeating user enumeration.
- **Account Lockout:** 5 consecutive failed login attempts trigger an automatic 15-minute temporary lockout.

### 3.2 Dual-Token Session Architecture
- **Access Token:** Short-lived JWT (~15 minutes) signed with HMAC-SHA256 (`JWT_ACCESS_SECRET`). Stored **only in frontend memory**, never written to `localStorage` or `sessionStorage`.
- **Refresh Token:** Long-lived (7 days) cryptographically random 32-byte opaque token.
  - Stored in an `httpOnly`, `SameSite=Strict` cookie (`Secure` flag enforced outside development).
  - Stored in the database **only as a SHA-256 hash** (`refresh_token_hash`).
- **Token Rotation & Reuse Detection (RFC 6819):**
  - Every call to `/api/v1/auth/refresh` issues a new refresh token and rotates the database hash.
  - If an already-rotated or revoked refresh token hash is presented, the system detects a token hijack and immediately revokes all sessions in that `family_id`.
- **Instant Revocation:** Suspending a user or calling `/auth/logout-all` invalidates sessions instantly; the authentication middleware verifies active status in PostgreSQL on every request.

### 3.3 Two-Factor Authentication (RFC 6238 TOTP)
- **Mandatory for Platform Super Admins.**
- **Secret Encryption:** TOTP secrets are encrypted at rest with AES-256-GCM using `MFA_ENCRYPTION_KEY` from `.env`.
- **Recovery Codes:** 10 single-use 10-character alphanumeric recovery codes stored hashed (SHA-256).
- **Two-Step Login:** If MFA is required, `/auth/login` returns `mfa_required: true` with a short-lived (5-minute) challenge token. Verification via TOTP code or recovery code completes the login.

---

## 4. Role-Based Access Control (RBAC)

### 4.1 Default-Deny Authorization
- Permissions are code-defined as `module.resource.action` (e.g. `user.account.create`, `org.organization.suspend`, `role.role.edit`, `audit.log.view`).
- Routes without an explicit permission policy are denied by default.
- `RequirePermission("module.resource.action")` middleware checks the actor's verified permissions. Platform admins possess wildcard access.

### 4.2 System Roles & Custom Roles
- **Super Admin (Platform only):** Unrestricted access across all tenants.
- **Org Admin:** Administrative control restricted to actor's organization and sub-tenants.
- **Staff:** Resource viewing and user invitations.
- **Read-only:** Read-only access.
- **Custom Roles:** Created, cloned, edited, and deleted per organization. System role templates cannot be edited or deleted.
- **Privilege Escalation Guard:** An actor cannot create or update a role with permissions the actor does not personally possess.

---

## 5. Immutable Audit Trail

- **Trigger-Enforced Immutability:** PostgreSQL trigger `audit_logs_immutable` raises an exception on any attempt to `UPDATE`, `DELETE`, or `TRUNCATE` the `audit_logs` table. Database permissions grant only `SELECT` and `INSERT` to the application user `znero_app`.
- **Transactional Consistency:** Audit log entries are written in the exact same database transaction as the audited state mutation. If the state mutation rolls back, the audit entry rolls back.
- **Sensitive Data Redaction:** Passwords, password hashes, JWTs, refresh tokens, TOTP secrets, and recovery codes are automatically redacted recursively from before/after state payloads before storage.

---

## 6. Directory Layout

```
znero-international/
├── backend/                        # Go + Fiber modular monolith
│   ├── cmd/
│   │   ├── api/                    # HTTP API entry point (main.go)
│   │   ├── bootstrap/              # Super Admin bootstrap CLI tool
│   │   └── kafkatest/              # Standalone CLI test producer
│   ├── internal/                   # Private modular monolith boundaries
│   │   ├── app/                    # Composition root & HTTP router assembly
│   │   ├── audit/                  # Audit trail (repo, service, handler)
│   │   ├── auth/                   # Authentication & session engine
│   │   ├── config/                 # Typed env-based configuration
│   │   ├── diagnostics/            # Kafka diagnostics
│   │   ├── health/                 # Liveness & readiness probes
│   │   ├── org/                    # Multi-tenancy & organization hierarchy
│   │   ├── rbac/                   # RBAC default-deny engine
│   │   ├── masterdata/             # Platform reference data
│   │   │   ├── country/            # ISO 3166-1/ITU-T countries & policies
│   │   │   ├── operator/           # MCC/MNC mobile & fixed network carriers
│   │   │   ├── prefix/             # E.164 routing prefixes & priority
│   │   │   ├── approval/           # Dual-control maker-checker engine
│   │   │   └── importjob/          # Batch import service (CSV/XLSX/JSON)
│   │   ├── number/                 # Number intelligence engine
│   │   │   ├── trie.go             # Lock-free atomic in-memory prefix trie
│   │   │   ├── engine.go           # nyaruka/phonenumbers E.164 normalization
│   │   │   └── rules.go            # Deterministic transformation rule chain
│   │   ├── secrets/                # AES-256-GCM keyring & one-time credential tokens
│   │   ├── provider/               # Upstream providers, connections & coverage
│   │   ├── customer/               # Customer commercial profiles, API keys & SMPP binds
│   │   ├── senderid/               # Sender ID registry & approval workflow
│   │   ├── rating/                 # Rate cards, effective dating & margin protection
│   │   ├── ledger/                 # Double-entry ledger, holds, maker-checker & wallets
│   │   ├── billing/                # Invoices, gap-free sequences, lines & payments
│   │   ├── routing/                # In-memory routing engine, snapshot, strategies, rules & kill switch
│   │   ├── messaging/              # Message ingestion, partitioned storage, outbox dispatcher & state machine
│   │   ├── smpp/                   # Clean-room SMPP 3.4 PDU codec, session manager, SSRF dialer & mock SMSC
│   │   └── system/                 # Database schema & version metadata
│   ├── migrations/                 # Up/Down SQL migrations (golang-migrate)
│   │   ├── 000001_create_schema_info.*
│   │   ├── 000002_create_auth_tenancy_rbac_audit.*
│   │   ├── 000003_phase3_master_data.*
│   │   ├── 000004_phase3_import_approval.*
│   │   ├── 000005_phase3_authz_hardening.*
│   │   ├── 000006_phase4_providers_customers_credentials.*
│   │   ├── 000007_phase5_rating_billing_preflight.*
│   │   ├── 000008_phase6_wallets_ledger_billing.*
│   │   ├── 000009_phase7_routing.*
│   │   └── 000010_phase8_messaging.*
│   └── pkg/                        # Reusable domain-agnostic packages
│       ├── crypto/encrypt/         # AES-256-GCM encryption
│       ├── crypto/password/        # Argon2id password hashing
│       ├── crypto/totp/            # RFC 6238 TOTP & recovery codes
│       ├── database/               # PostgreSQL pgxpool manager
│       ├── httpx/                  # Fiber envelopes & security filters
│       ├── kafkaclient/            # Kafka franz-go wrapper
│       ├── logx/                   # Slog structured logging
│       ├── tenant/                 # Server-derived tenant context
│       └── tokens/                 # JWT & opaque token manager
│
├── frontend/                       # SvelteKit + TypeScript + Tailwind CSS
│   ├── src/
│   │   ├── lib/
│   │   │   ├── api/                # Typed API client with silent refresh
│   │   │   ├── auth.svelte.ts      # Reactive auth manager (Runes)
│   │   │   ├── components/         # Reusable UI states & layout shell
│   │   │   └── types/              # TypeScript API contracts
│   │   └── routes/
│   │       ├── master-data/        # Phase 3 Master Data & Intelligence
│   │       │   ├── countries/      # ISO country reference & policies
│   │       │   ├── operators/      # Carrier registry & MCC/MNC
│   │       │   ├── prefixes/       # Dial prefix mapping & priority
│   │       │   ├── import/         # Import center (upload, preview, history)
│   │       │   ├── pending-changes/# Maker-checker approval queue
│   │       │   ├── number-lookup/  # Single & bulk number intelligence
│   │       │   └── transform-rules/# Number transformation rules & chain simulator
│   │       ├── wallets/            # Customer & reseller wallets, ledger statements, holds
│   │       │   └── [id]/           # Wallet details, ledger tabs, topup/adjust/freeze
│   │       ├── invoices/           # Invoicing center, draft creation, issuance & payments
│   │       │   └── [id]/           # Printable HTML invoice document preview
│   │       ├── reconciliation/     # Ledger reconciliation dashboard & auto-repair
│   │       ├── rates/              # Rate cards, buy/sell rate comparison, simulator
│   │       ├── routing/            # Phase 7 Routing Engine Workbench
│   │       │   ├── plans/          # Route plans list & modal
│   │       │   │   └── [id]/       # Route plan details, entries, assignments, validation, history
│   │       │   ├── assignments/    # Customer route plan assignments
│   │       │   ├── rules/          # Pre-routing override rules
│   │       │   ├── simulator/      # Route resolution tester & candidate trace
│   │       │   └── kill-switch/    # Emergency provider connection disablement
│   │       ├── login/              # Sign in page
│   │       ├── mfa/challenge/      # 2FA challenge page
│   │       ├── mfa/setup/          # 2FA setup & recovery codes page
│   │       ├── forgot-password/    # Password reset request
│   │       ├── reset-password/     # Password reset confirmation
│   │       ├── accept-invitation/  # User invitation acceptance
│   │       ├── organizations/      # Tenant hierarchy manager
│   │       ├── users/              # User directory & role assignments
│   │       ├── roles/              # RBAC role builder & permission matrix
│   │       ├── sessions/           # Active sessions & login activity
│   │       ├── settings/password/  # Change password page
│   │       ├── audit/              # Immutable audit trail & diff viewer
│   │       └── +page.svelte        # System & infrastructure status dashboard
│
├── scripts/                        # Safe PowerShell automation scripts
│   ├── bootstrap-admin.ps1         # First-time Super Admin bootstrap
│   ├── dev.ps1                     # Single daily startup script
│   ├── migrate-down.ps1            # Database migration down
│   ├── migrate-up.ps1              # Database migration up
│   ├── run-backend.ps1             # Backend dev runner
│   ├── run-frontend.ps1            # Frontend dev runner
│   ├── seed-dev-data.ps1           # Development test account seeder
│   ├── seed-master-sample.ps1      # Dev sample operator & prefix seeder
│   ├── seed-providers-sample.ps1   # Phase 4 dev sample provider seeder
│   ├── seed-ratessample.ps1        # Phase 5 dev sample rate card seeder
│   ├── seed-wallets-sample.ps1     # Phase 6 dev sample wallets & invoices seeder
│   ├── seed-routing-sample.ps1     # Phase 7 dev sample route plans, assignments & rules seeder
│   ├── reconcile-ledger.ps1        # Phase 6 double-entry ledger reconciliation tool
│   ├── setup-db.ps1                # Idempotent DB setup & migrations
│   ├── start-kafka.ps1             # Kafka background starter
│   ├── status.ps1                  # Live component status reporter
│   ├── stop-all.ps1                # Clean shutdown script
│   ├── stop-kafka.ps1              # Kafka clean shutdown
│   └── test.ps1                    # Unified test runner
│
└── docs/                           # Project documentation
    ├── ARCHITECTURE.md
    ├── DATA_POLICY.md
    ├── IMPLEMENTATION_STATUS.md
    ├── MODULE_REGISTRY.md
    ├── RUNNING_LOCALLY.md
    └── SECURITY_NOTES.md
```

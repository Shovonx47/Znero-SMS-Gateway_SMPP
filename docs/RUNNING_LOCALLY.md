# Running Znero International Locally

This document describes how to set up, start, test, and maintain the local development environment for **Znero International** on Windows (PowerShell) running natively without Docker.

---

## 1. Prerequisites Checklist

Ensure the following tools are installed on your machine and reachable on `PATH`:
- **Go**: 1.22+ (`go version`)
- **Node.js**: LTS (`node -v`, `npm -v`)
- **PostgreSQL**: 16+ running locally on port 5432
- **Apache Kafka**: 3.7+ in KRaft mode at `C:\kafka` (port 9092)
- **golang-migrate**: (`migrate -version`)
- **Git**: (`git --version`)

Configuration parameters live in `.env` at the repository root. A template is provided in `.env.example`.

---

## 2. First-Time Setup (In Order)

Run the following commands in PowerShell from the repository root:

### Step 1: Database Setup and Migrations
Ensures PostgreSQL is listening, verifies the `znero_app` role and `znero_dev` database, generates `SECRETS_ENCRYPTION_KEY` (32-byte AES-256-GCM base64 key) and `SECRETS_KEY_ID=v1` in `.env` if missing, and applies all pending migrations (including immutability triggers and permission seeds):
```powershell
.\scripts\setup-db.ps1
```
> [!TIP]
> You can also generate a new 32-byte base64 `SECRETS_ENCRYPTION_KEY` manually in PowerShell at any time:
> `[Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Minimum 0 -Maximum 256 }))`

### Step 2: Bootstrap Initial Platform Super Admin
Interactively prompts for the Super Admin email and a secure password (minimum 12 characters, non-common), creates the platform root organization, seeds system role definitions, generates TOTP MFA enrollment keys and 10 single-use recovery codes, and audits the bootstrap event:
```powershell
.\scripts\bootstrap-admin.ps1
```
> [!IMPORTANT]
> Save the displayed **TOTP Secret** and **10 Single-Use Recovery Codes** immediately in your password manager. To prevent hijacking, this command is strictly idempotent and aborts if a Super Admin already exists.

### Step 3: Seed Reference Master Data (Authoritative ISO Countries & Labeled Dev Samples)
Seeds baseline ISO 3166-1/ITU-T E.164 country reference data (249 countries via the import service) and clearly labeled dev sample operators/prefixes (guarded by `APP_ENV=development`):
```powershell
.\scripts\seed-master-sample.ps1
```
> [!NOTE]
> To remove the dev sample operators and prefixes:
> `.\scripts\seed-master-sample.ps1 -Cleanup`

### Step 4 (Optional): Seed Development Test Accounts
Seeds labeled fake development test accounts (including platform Maker and Checker dual-control accounts) for automated & local subtree testing (strictly prohibited outside development):
```powershell
.\scripts\seed-dev-data.ps1
```
> [!NOTE]
> To clean up fake test accounts:
> `.\scripts\seed-dev-data.ps1 -Cleanup`

### Step 5 (Optional): Seed Phase 4 Sample Providers, Coverage, Customer Profiles & Sender IDs
Seeds clearly labeled fictional `DEV SAMPLE` providers (`DEV_ALPHA`, `DEV_BETA`), connection configs (`example.com` endpoints with runtime-generated random encrypted passwords), coverage rows, a sample customer commercial profile, and a sample Sender ID via the service layer (strictly guarded by `APP_ENV=development`):
```powershell
.\scripts\seed-providers-sample.ps1
```
> [!NOTE]
> To clean up all `DEV SAMPLE` Phase 4 providers, connections, coverage, customer profiles, and sender IDs:
> `.\scripts\seed-providers-sample.ps1 -Cleanup`

### Step 6 (Optional): Seed Phase 6 Sample Wallets, Invoices, and Payments
Seeds clearly labeled fictional `DEV SAMPLE` customer wallets, top-ups, adjustments, a draft invoice, an issued invoice, and partial payment through the service layer (strictly guarded by `APP_ENV=development`):
```powershell
.\scripts\seed-wallets-sample.ps1
```
> [!NOTE]
> To clean up all `DEV SAMPLE` Phase 6 sample data without deleting append-only ledger rows (freezes sample wallets and voids sample draft invoices):
> `.\scripts\seed-wallets-sample.ps1 -Cleanup`

### Step 7: Reconcile Ledger and Balance Integrity
Verifies all wallet cached balance rows against raw double-entry `ledger_entries` sums:
```powershell
.\scripts\reconcile-ledger.ps1
```
> [!TIP]
> If any cached balance drift is detected, pass `-Repair` to recalculate and fix cached rows from the raw entries:
> `.\scripts\reconcile-ledger.ps1 -Repair`

### Step 8 (Optional): Seed Phase 7 Sample Route Plans, Assignments & Rules
Seeds clearly labeled fictional `DEV SAMPLE` route plans (Priority and Least Cost strategies), route plan entries, customer route assignments, and sample routing rules through the service layer (strictly guarded by `APP_ENV=development`):
```powershell
.\scripts\seed-routing-sample.ps1
```
> [!NOTE]
> To clean up all `DEV SAMPLE` Phase 7 sample routing data:
> `.\scripts\seed-routing-sample.ps1 -Cleanup`

### Step 9 (Optional): Seed Phase 8 Sample Messaging Carrier, Connection & Routing Plan
Seeds clearly labeled fictional `DEV SAMPLE` provider connection configured for local mock SMSC (`127.0.0.1:2775`, system_id: `mock_smsc`, password: `mock_pass`), an active route plan, buy/sell rate cards, and pre-funded customer wallet for end-to-end SMS testing (strictly guarded by `APP_ENV=development`):
```powershell
.\scripts\seed-messaging-sample.ps1
```
> [!NOTE]
> To clean up all `DEV SAMPLE` Phase 8 sample routing and provider data (retains append-only ledger entries):
> `.\scripts\seed-messaging-sample.ps1 -Cleanup`

---

## 3. Daily Development Workflow

### Single Daily Start Command
Start Kafka (KRaft), the Go backend API, and the SvelteKit frontend in one command:
```powershell
.\scripts\dev.ps1
# Or include the Mock SMPP SMSC simulator:
.\scripts\dev.ps1 -WithSmppMock
```
This script starts all components, waits for the backend `/readyz` probe to report readiness, and displays clickable URLs:
- **Frontend Web UI**: [http://localhost:5173](http://localhost:5173)
  - **Countries**: [http://localhost:5173/master-data/countries](http://localhost:5173/master-data/countries)
  - **Operators**: [http://localhost:5173/master-data/operators](http://localhost:5173/master-data/operators)

### Standalone SMPP Mock SMSC Simulator
For protocol testing and development against upstream carrier simulators without starting the full web stack:
```powershell
# Default listener on 127.0.0.1:2775 (system_id: mock_smsc, password: mock_pass)
.\scripts\run-smpp-mock.ps1

# Custom error injection (e.g. ESME_RTHROTTLED 0x58) and delayed DLRs:
.\scripts\run-smpp-mock.ps1 -Addr "127.0.0.1:2775" -ErrorCode "0x58" -DlrMode "delayed"
```
  - **Number Prefixes**: [http://localhost:5173/master-data/prefixes](http://localhost:5173/master-data/prefixes)
  - **Pending Approvals**: [http://localhost:5173/pending-changes](http://localhost:5173/pending-changes)
  - **Wallets & Balances**: [http://localhost:5173/wallets](http://localhost:5173/wallets)
  - **Invoices & Billing**: [http://localhost:5173/invoices](http://localhost:5173/invoices)
  - **Ledger Reconciliation**: [http://localhost:5173/reconciliation](http://localhost:5173/reconciliation)
  - **Messages Explorer**: [http://localhost:5173/messages](http://localhost:5173/messages)
  - **SMPP Sessions**: [http://localhost:5173/messaging/smpp-sessions](http://localhost:5173/messaging/smpp-sessions)
  - **Queue Backlog Monitor**: [http://localhost:5173/messaging/queue-monitor](http://localhost:5173/messaging/queue-monitor)
  - **Unmatched DLRs**: [http://localhost:5173/messaging/unmatched-dlrs](http://localhost:5173/messaging/unmatched-dlrs)
  - **Messaging Reconciliation**: [http://localhost:5173/messaging/reconciliation](http://localhost:5173/messaging/reconciliation)
  - **DLR Mappings**: [http://localhost:5173/messaging/dlr-mappings](http://localhost:5173/messaging/dlr-mappings)
- **Backend API Root**: [http://127.0.0.1:8080/api/v1](http://127.0.0.1:8080/api/v1)
- **Process Liveness**: [http://127.0.0.1:8080/healthz](http://127.0.0.1:8080/healthz)
- **Dependency Readiness**: [http://127.0.0.1:8080/readyz](http://127.0.0.1:8080/readyz)

### Checking Live Status
To inspect the running status of PostgreSQL, Kafka, Backend HTTP, and Frontend:
```powershell
.\scripts\status.ps1
```

### Clean Shutdown
To halt all running development processes (Kafka, backend, and frontend) cleanly:
```powershell
.\scripts\stop-all.ps1
```

---

## 4. Running Tests

Run the unified test suite across Go and SvelteKit:

### Unit Tests & Typechecks (Fast, Offline)
Runs `go vet`, all Go module unit tests, `svelte-check` type analysis, and Vitest frontend unit tests:
```powershell
.\scripts\test.ps1
```

### Full Integration Test Suite
Executes integration tests against the live PostgreSQL database and running Kafka broker:
```powershell
.\scripts\test.ps1 -Integration
```

### Targeted Test Runs
```powershell
.\scripts\test.ps1 -BackendOnly               # Go vet and Go unit tests
.\scripts\test.ps1 -BackendOnly -Integration  # Backend unit + live DB/Kafka tests
.\scripts\test.ps1 -FrontendOnly              # svelte-check + Vitest unit tests
```

### End-to-End Playwright Tests
Executes the Playwright suite covering multi-tenant isolation, Maker-Checker workflows, and invoice payment cycles:
```powershell
cd frontend
npx playwright test e2e/phase6-wallets-billing.spec.ts
```

---

## 5. Development Database Reset

To completely roll back and reapply database migrations from scratch:

```powershell
# Step 1: Roll back all applied migrations
.\scripts\migrate-down.ps1

# Step 2: Reapply all migrations up to current version
.\scripts\migrate-up.ps1

# Step 3: Re-bootstrap the Super Admin
.\scripts\bootstrap-admin.ps1
```

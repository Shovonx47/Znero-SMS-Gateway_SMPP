# Phase 6: Wallets, Double-Entry Ledger, Credit Enforcement & Billing Design

## 1. Architectural Overview & Philosophy

The Znero billing engine is designed for high-throughput telecom traffic (SMS, WhatsApp, RCS) requiring strict financial guarantees. The ledger follows classical double-entry accounting with strict immutability, zero floating-point math, row-level concurrency locking, maker-checker authorization, and multi-tenant isolation.

```mermaid
flowchart TD
    subgraph Client / Service
        API[API / Handler Layer]
        Svc[Ledger / Billing Service]
        Auth[RBAC & Tenancy Enforcement]
    end

    subgraph Database Transaction (ACID)
        Lock[Lock Wallet: SELECT ... FOR UPDATE]
        Check[Credit Enforcement: Prepaid >= 0, Postpaid >= -Limit]
        Audit[Audit Entry in same TX]
        Entries[Append Immutable Ledger Entries]
        Tx[Append Ledger Transaction]
        Bal[Update Cached Balance]
    end

    API --> Auth --> Svc --> Lock --> Check --> Entries --> Tx --> Bal --> Audit
```

---

## 2. Money Representation

### Choice: `NUMERIC(18, 4)` & Arbitrary-Precision Decimal
In telecom infrastructure (wholesale SMS termination, DID routing), rates are priced in fractional cents (e.g., `$0.0035` or `$0.0125` per SMS segment). 
- **Why Not Floats**: IEEE 754 floating-point types (`float32`, `float64`, `DOUBLE PRECISION`) suffer from binary representation inaccuracies (e.g., `0.1 + 0.2 != 0.3`), resulting in cumulative rounding drift across millions of messaging transactions.
- **Why `NUMERIC(18, 4)` Over 2-Decimal Minor Cents**: A traditional 2-decimal integer (cents) cannot express wholesale SMS segment pricing without artificial sub-unit scaling (e.g., microcents). `NUMERIC(18, 4)` in PostgreSQL paired with `github.com/shopspring/decimal` in Go provides exact fixed-scale decimal arithmetic without truncation or rounding error, while cleanly formatting standard telecom invoices up to 4 decimal places.
- **Currencies**: Multicurrency-ready, anchored by the customer wallet currency (`USD`, `EUR`, `GBP`). All transactions on a given wallet and ledger account occur strictly in the designated currency; cross-currency auto-conversion is explicitly forbidden in this phase.

---

## 3. Account Chart & Double-Entry Principles

Every financial movement requires a balanced `ledger_transactions` record containing at least two `ledger_entries` such that:

$$\sum \text{Debits} = \sum \text{Credits}$$

### Standard Account Chart
| Code | Account Type | Normal Balance | Purpose |
|------|-------------|----------------|---------|
| `CUSTOMER_FUNDS` | Liability | Credit | Represents customer prepaid funds or balances held on platform. |
| `PLATFORM_REVENUE` | Revenue | Credit | Earned revenue from delivered traffic and subscription charges. |
| `ADJUSTMENTS` | Equity / Expense | Debit | Clearing account for manual billing credits/debits by platform staff. |
| `SUSPENSE` | Clearing | Zero | Interim holding account for unallocated payments and reversals. |
| `WALLET:<ID>` | Customer Sub-ledger | Credit | Customer-specific account tracking running balance. |

### Entry Directions for Standard Operations
- **Prepaid Top-Up ($100.0000)**:
  - `Debit`: `CUSTOMER_FUNDS` (or Clearing/Cash) `$100.0000`
  - `Credit`: `WALLET:<ID>` `$100.0000`
- **Manual Debit Adjustment ($25.0000)**:
  - `Debit`: `WALLET:<ID>` `$25.0000`
  - `Credit`: `ADJUSTMENTS` `$25.0000`
- **Traffic Charge / Service Fee ($50.0000)**:
  - `Debit`: `WALLET:<ID>` `$50.0000`
  - `Credit`: `PLATFORM_REVENUE` `$50.0000`

---

## 4. Supported Transaction Types

1. `topup`: Crediting a wallet via bank wire, manual payment, or customer balance replenishment.
2. `adjustment`: Manual credit or debit correction applied by authorized platform personnel.
3. `charge`: Traffic consumption, message delivery, or recurring subscription fees.
4. `refund`: Customer funds returned from revenue or platform reserves.
5. `reversal`: Correction of a prior erroneous transaction. Points to `original_transaction_id`. Reversing an already reversed transaction is strictly refused.
6. `hold_capture`: Settlement of a previously pre-authorized hold (supports full or partial capture).
7. `transfer`: Atomic inter-wallet funds movement.

---

## 5. Append-Only Database Immutability

Ledger tables are legally auditable books of record. PostgreSQL database triggers enforce immutability:
- `ledger_accounts`: UPDATE and DELETE are blocked by trigger `trg_ledger_accounts_immutable`.
- `ledger_transactions`: UPDATE and DELETE are blocked by trigger `trg_ledger_transactions_immutable`.
- `ledger_entries`: UPDATE and DELETE are blocked by trigger `trg_ledger_entries_immutable`.
- `invoices`: Once status is `issued` or `paid`, modifications are blocked by `trg_invoices_immutable`.
- `invoice_lines`: Rows linked to non-draft invoices cannot be updated or deleted (`trg_invoice_lines_immutable`).

Any financial adjustment must be recorded as an append-only transaction or a formal credit note (`CN-YYYY-XXXXXX`).

---

## 6. Concurrency Strategy & Credit Enforcement

High-concurrency environments must prevent balance overdraft and race conditions:
1. **Row-Level Locking**: When modifying a wallet or placing/capturing holds, the service acquires an exclusive row lock:
   ```sql
   SELECT id, balance, status, account_type, credit_limit 
   FROM wallets 
   WHERE id = $1 FOR UPDATE;
   ```
2. **Strict Balance Limits**:
   - **Prepaid Accounts**:
     $$\text{Available Balance} = \text{Cached Balance} - \text{Active Holds} \ge 0$$
   - **Postpaid Accounts**:
     $$\text{Available Balance} = \text{Cached Balance} - \text{Active Holds} + \text{Credit Limit} \ge 0$$
     *(Postpaid balance cannot drop below $-\text{Credit Limit}$)*.
3. **Credit Limit Source of Truth**: Read from the customer profile; not duplicated as an editable field in the wallet.
4. **Stress Verification**: Verified with parallel goroutines testing 50+ concurrent debit transactions against a single wallet without overspending.

---

## 7. Holds (Two-Phase Reserve & Settle)

Holds allow pre-authorization of funds before message dispatch without posting entries to the general ledger:
1. **Place Hold**:
   - Verifies sufficient available balance.
   - Inserts row into `holds` table with status `active` and expiry timestamp.
   - Decreases *Available Balance* immediately while leaving *Ledger Balance* unchanged.
2. **Capture Hold**:
   - Posts a `hold_capture` ledger transaction for the captured amount.
   - Releases the remainder of the hold if captured amount is less than the hold amount (partial capture).
   - Updates hold status to `captured`.
3. **Release Hold**:
   - Releases the held funds back to available balance; updates hold status to `released`.
4. **Hold Expiry Job**:
   - Automated CLI worker: `backend/cmd/expireholds/main.go` and scheduled script `scripts/expire-holds.ps1`.
   - The job invokes `ledgerService.ExpireOldHolds(ctx)`, which queries:
     ```sql
     SELECT id, wallet_id, amount FROM holds 
     WHERE status = 'active' AND expires_at <= NOW()
     FOR UPDATE SKIP LOCKED;
     ```
   - For each expired hold, sets `status = 'expired'`. Because available balance is dynamically computed as:
     $$\text{Available Balance} = \text{Cached Balance} - \sum \text{Active Holds} + \text{Credit Limit}$$
     transitioning the status from `active` to `expired` instantly restores the customer's available balance without requiring general ledger writes.
   - Attempts to capture an expired hold are strictly blocked (`ErrHoldNotActive`).

---

## 8. Idempotency Guarantees & Rate Limiting

### Idempotency
All balance-moving and billing endpoints accept an `idempotency_key`:
- **Ledger Scope**: `(tenant_org_id, transaction_type, idempotency_key)` is enforced by a database unique index.
- **Billing Scope**: `(tenant_org_id, idempotency_key)` is enforced on `payments` and `invoices`.
- **Replay Behavior**:
  - Request with identical parameters returns the original successful transaction envelope (`200 OK`).
  - Request with identical key but mismatched parameters (different amount, currency, or wallet) returns `409 Conflict`.

### Money-Moving Rate Limiting & Upper Bounds
- **Rate Limiting**: Applied to all balance-altering endpoints (`POST /api/v1/wallets/{id}/topup`, `POST /api/v1/wallets/{id}/adjust`, `POST /api/v1/wallets/{id}/holds`, `POST /api/v1/billing/invoices/{id}/pay`, etc.) via `httpx.MoneyRateLimiter(60, time.Minute)` (max 60 requests per minute per IP/tenant).
- **Upper Bounds**: Enforced strictly at the service layer:
  - Single transaction maximum bound: `$1,000,000.0000` (`MaxTransactionAmount`).
  - Single invoice / payment maximum bound: `$1,000,000.0000`.
  - Non-positive or out-of-range amounts are rejected immediately with `ErrInvalidAmount` or `ErrAmountExceedsMaximum`.

---

## 9. Maker-Checker Dual-Control Workflow

To protect against internal fraud and operator error:
- Staff actions that move money (`wallet.topup.create`, `wallet.adjustment.create`, `wallet.freeze`, `billing.invoice.void`) without `wallet.override` are staged as `approval_requests`.
- A separate user possessing `wallet.approve` or `billing.invoice.void` must approve the request before funds are posted.
- **Dual-Control Rule**: The maker who created the request is cryptographically and procedurally blocked from approving their own request.
- **Queue Integration**: Wallet top-ups, adjustments, wallet unfreezes, and invoice voids are fully integrated into the global `/masterdata/pending-changes` review queue.

---

## 10. Billing Basics & Invoicing Engine

### Gap-Free Invoice Numbering Architecture
Standard PostgreSQL sequences (`nextval()`) are **not gap-free**: when a database transaction rolls back, sequence increments are never rolled back, permanently burning numbers and leaving regulatory audit holes.

To guarantee strictly gap-free numbering:
1. **Dedicated Counter Table**:
   ```sql
   CREATE TABLE invoice_sequences (
       issuer_org_id UUID NOT NULL REFERENCES organizations(id),
       series VARCHAR(16) NOT NULL DEFAULT 'INV',
       year INT NOT NULL,
       current_val BIGINT NOT NULL DEFAULT 0,
       PRIMARY KEY (issuer_org_id, series, year)
   );
   ```
2. **Issue Transaction Lock**:
   During `IssueInvoice(ctx, id)`:
   ```sql
   SELECT current_val 
   FROM invoice_sequences 
   WHERE issuer_org_id = $1 AND series = $2 AND year = $3 
   FOR UPDATE;
   ```
   The counter row is locked exclusively. If the issue transaction succeeds, `current_val` increments and commits. If the transaction rolls back for *any reason*, the row update rolls back atomically with the transaction, ensuring **no number is consumed and zero gaps are created**.
3. **Year Rollover**:
   - On January 1st (e.g., Year 2027), the query attempts to find `year = 2027`.
   - If not present, it executes:
     ```sql
     INSERT INTO invoice_sequences (issuer_org_id, series, year, current_val)
     VALUES ($1, $2, 2027, 0)
     ON CONFLICT (issuer_org_id, series, year) DO NOTHING;
     ```
   - It locks the row and increments to `1`, producing `INV-2027-000001` (or `CN-2027-000001` for credit notes).
   - Sequence numbering resets cleanly each calendar year per issuer organization.

### Payments Accounting Walkthrough (Prepaid vs Postpaid)

#### A. Postpaid Lifecycle
1. **Usage Incurred (Phase 8 Ledger Operations)**:
   As messages are dispatched, charges are posted in real-time to the ledger:
   - `Debit`: `WALLET:<CustID>` `$100.0000`
   - `Credit`: `PLATFORM_REVENUE` `$100.0000`
   - *Wallet Ledger Balance*: `-$100.0000` (Negative, backed by customer credit limit).
2. **Invoice Issuance**:
   At billing period close, the platform issues an invoice totaling `$100.0000` covering the incurred usage.
   - *Ledger Effect on Issuance*: **None**. The usage charges are *already* in the general ledger. Issuing the invoice creates the formal legal document and sets `invoices.outstanding_balance = $100.0000`. Posting a second ledger debit upon issue would result in double-charging the customer.
3. **Invoice Payment Recorded (Postpaid Rule & Manual Lines Protection)**:
   The customer pays `$100.0000` via Bank Transfer:
   - **Postpaid Owed-Debt Rule**: A postpaid wallet operates on credit limit and its balance tracks accumulated negative debt (`balance <= 0.0000`). It must **never** be artificially inflated into positive spendable balance by paying an invoice that contains manual lines (e.g. setup fees, dedicated short code rentals, support contracts) with no matching prior ledger usage debit.
   - When payment is processed on a postpaid customer:
     - `Wallet Credit = MIN(Payment Amount, ABS(Wallet Balance))` (capped at what is actually owed on the wallet).
     - `Revenue Credit = Payment Amount - Wallet Credit` (the excess settling manual invoice lines).
   - **Ledger Entries**:
     - `Debit`: `CUSTOMER_FUNDS` (Bank/Cash Clearing) `Payment Amount`
     - If `Wallet Credit > 0`: `Credit`: `WALLET:<CustID>` `Wallet Credit` (settling debt up to `$0.0000`).
     - If `Revenue Credit > 0`: `Credit`: `PLATFORM_REVENUE` `Revenue Credit` (earned platform revenue for manual service charges).
   - *Invoice State*: `amount_paid` becomes `$100.0000`, `outstanding_balance` becomes `$0.0000`, status transitions to `paid`.
   - *Wallet Ledger Balance*: Restored to at most `$0.0000`. The customer gains **zero unowed spendable credit**.

#### B. Prepaid Lifecycle
1. **Pre-Billing / Top-Up Invoice Issuance**:
   A prepaid customer requests or receives an invoice for `$500.0000` balance replenishment:
   - *Ledger Effect on Issuance*: **None**. No funds have been received, and no service has been delivered. Status is `issued`, `outstanding_balance = $500.0000`.
2. **Invoice Payment Recorded**:
   The customer remits `$500.0000`:
   - `Debit`: `CUSTOMER_FUNDS` (Bank/Cash Clearing) `$500.0000`
   - `Credit`: `WALLET:<CustID>` `$500.0000`
   - *Invoice State*: Status transitions to `paid`.
   - *Wallet Ledger Balance*: Increases from `$0.0000` to `+$500.0000`.
3. **Usage Incurred**:
   As messages are dispatched, charges debit the wallet down toward `$0.0000`.

#### C. Proof That Payments Cannot Create Spendable Credit Never Owed
The system mathematically and procedurally prevents unauthorized credit creation:
1. **Invoice State Gate**: Payments are permitted *only* on invoices in `issued` status (`ErrInvoiceCannotPay`). Draft, paid, or void invoices reject payment attempts.
2. **Overpayment Bounds**: Every payment enforces:
   $$\text{Payment Amount} \le \text{Invoice Outstanding Balance}$$
   Attempting to pay even `$0.0001` over the outstanding balance fails immediately with `ErrPaymentExceedsTotal`.
3. **Partial Payments**: A partial payment reduces `outstanding_balance` and increments `amount_paid`, but keeps status as `issued`.
4. **Void Protection**: Voiding an invoice releases unpaid obligations; subsequent payment attempts are blocked.
5. **Idempotency**: All payments enforce unique idempotency keys per tenant; network retries return the original payment without re-crediting the wallet.
6. **Mathematical Guarantee**: Because a customer cannot pay more than `total_amount` of a legitimate invoice, payments can never inject unbacked credit into the customer wallet.

---

## 11. Reseller Wallets & Wallet Lifecycle

### Reseller Wallets Decision
In Phase 6, **resellers do not have active wallets**:
- Ground rules explicitly state: *"A reseller may NOT credit its own customers' wallets in this phase unless you can implement a safe reseller-funded transfer... If not, defer it and say so."*
- Resellers currently have **read-only visibility** over child customer wallets, statements, and invoices.
- Reseller balance creation, wholesale credit lines, and two-leg funds settlement (Reseller Wallet $\rightarrow$ Customer Wallet) are cleanly deferred to Phase 8.

### Wallet Creation & Management Lifecycle
1. **Customer Wallets**: Created via `POST /api/v1/wallets` or seeded for DEV SAMPLE organizations. One wallet per customer organization per currency.
2. **Status Lifecycle**: `active` $\leftrightarrow$ `frozen` $\rightarrow$ `closed`.
   - Freezing blocks all outgoing debits and hold placements.
   - Unfreezing requires Maker-Checker dual control (`wallet.unfreeze`).
3. **Explanation of Historical Test Wallets**:
   Integration tests in earlier phases (Phases 2–5) created test organizations (e.g., `Tenant Test Org A`, `Escalation Test Org`). The initial DEV SAMPLE seed did not filter by customer name, generating wallets for all active organizations in the database. The seed tool has been updated to filter strictly on `(name ILIKE '%BlueStar%' OR name ILIKE '%Fintech%' OR name ILIKE '%DEV SAMPLE%')`. Tests in `internal/ledger` and `internal/billing` create dedicated test fixtures on-the-fly.


---

## 11. Multi-Tenant Isolation & Role-Based Access Control

### Access Boundaries
- **Platform Admins**: Global view across all organizations, wallets, transactions, holds, invoices, and reconciliation reports.
- **Resellers**: Strictly restricted to viewing direct child customers' wallets, invoices, and statements (read-only). Direct cross-tenant access to foreign customer wallets or invoices returns `403 Forbidden`. Reseller-to-customer direct balance creation without backing reseller balance is deferred to future multi-leg settlement.
- **Customers**: Strictly restricted to viewing their own organization's wallet, transaction statement, and issued invoices. Platform administrative endpoints return `403 Forbidden`.

### Confidentiality & Anti-Leakage
- Provider buy rates, upstream supplier IDs, and wholesale margins are stripped at the data layer and never rendered in customer or reseller statements or invoice line items.
- Formula-injection sanitization (`=`, `+`, `-`, `@` escaped with single-quote prefix) is applied across all CSV and JSON statement exports.

---

## 12. Verification & Reconciliation Tooling

- **Reconciliation Engine** (`backend/cmd/reconcile` and `scripts/reconcile-ledger.ps1`):
  - Recalculates exact balances by summing raw `ledger_entries` across all wallets.
  - Compares computed balances against `wallets.balance` cache rows.
  - Reports all discrepancies and supports `--repair` mode to restore corrupted cached balances.
- **Dev Sample Seeder** (`scripts/seed-wallets-sample.ps1`):
  - Seeds wallets, transactions, invoices, and payments via official service layer methods.
  - Strictly restricted to `APP_ENV=development`.
  - Supports non-destructive `-Cleanup` mode (freezing sample wallets and voiding sample draft invoices; preserving append-only ledger integrity).


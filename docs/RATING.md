# Phase 5: Rating Engine & Rate Cards Architecture and Guide

This document describes the design, implementation, and operational workflows of the **Rating Engine & Rate Cards** subsystem in the Znero International SMS Gateway.

---

## 1. Executive Summary & Capabilities

The Rating Engine is the financial core of Znero's billing and routing pipeline. It determines the exact cost (for supplier buy sheets) and customer charge (for client sell sheets) for every SMS routed through the gateway.

Key capabilities include:
- **Provider Buy Cards vs Customer Sell Cards**: Segregated upstream wholesale supplier costs and downstream customer pricing.
- **Reseller Multi-Tenant Model**: Platform operators assign wholesale cost sheets to resellers; resellers create branded sell cards for their sub-accounts with guaranteed profit margin floors.
- **Half-Open Effective Dating `[effective_from, effective_to)`**: Deterministic microsecond temporal resolution preventing rate overlaps and race conditions during pricing transitions.
- **Hierarchical Specificity**: Fallback from exact Operator match (`MCC`/`MNC`) to Country Default with message-type specificity.
- **E.164 Number Resolution**: Longest-prefix match trie mapping arbitrary phone numbers to destination country and carrier in sub-microsecond time.
- **Ultra-Fast In-Memory Engine**: Thread-safe in-memory rate snapshot delivering sub-microsecond resolution (benchmarked at ~723ns/op) without database round-trips on hot routing paths.
- **Real-Time Margin Protection**: Instant validation blocking deficit rates (selling below lowest active provider buy rate or below reseller cost floor) with dual-control override capabilities.
- **High-Throughput Streaming CSV Import**: 64KB chunked scanner parsing and staging >1.29 million rates/sec.
- **Dual-Control Maker-Checker Workflow**: Mandatory four-eyes approval for rate card modifications, rate updates, and client assignments.
- **Full Versioning & Immutable Snapshots**: Complete rollback history on cards, rates, and customer assignments.

---

## 2. Core Domain Model

### 2.1 Rate Cards (`rate_cards`)
A Rate Card is a container representing an agreed SMS price sheet for a given currency and channel.

| Field | Type | Description |
|---|---|---|
| `id` | `UUID` | Primary key |
| `name` | `VARCHAR(255)` | Descriptive title (e.g. "Direct Route Tier 1") |
| `kind` | `VARCHAR(32)` | `provider_buy` (upstream cost) or `customer_sell` (client price) |
| `owner_org_id` | `UUID` | Owning organization (Platform or Reseller) |
| `provider_id` | `UUID NULL` | Associated upstream provider (for `provider_buy` cards) |
| `currency_code`| `VARCHAR(3)` | ISO 4217 currency code (e.g. `USD`, `EUR`, `GBP`) |
| `channel` | `VARCHAR(32)` | Communication channel (`sms`, `mms`, `whatsapp`) |
| `status` | `VARCHAR(32)` | `draft`, `active`, `inactive`, `archived` |
| `version` | `INT` | Optimistic locking counter |

### 2.2 Destination Rates (`rates`)
A Rate represents the cost or price per single SMS segment for a specific destination and message type.

| Field | Type | Description |
|---|---|---|
| `id` | `UUID` | Primary key |
| `card_id` | `UUID` | Reference to parent `rate_cards.id` |
| `country_id` | `UUID` | Reference to destination `countries.id` |
| `operator_id`| `UUID NULL` | Reference to `operators.id` (NULL = Country Default) |
| `message_type`| `VARCHAR(32)`| `default`, `promotional`, `transactional`, `otp` |
| `price` | `NUMERIC(18,6)` | Rate per SMS segment (stored to 6 decimal places) |
| `min_billing_note` | `TEXT` | Minimum billing interval or segment rules |
| `effective_from` | `TIMESTAMPTZ` | Start of validity period (inclusive) |
| `effective_to` | `TIMESTAMPTZ NULL` | End of validity period (exclusive, NULL = open-ended) |
| `status` | `VARCHAR(32)` | `draft`, `active`, `closed` |
| `version` | `INT` | Snapshot version |

### 2.3 Assignments
- **`customer_rate_assignments`**: Assigns a `customer_sell` rate card to a customer organization for a validity interval.
- **`reseller_cost_assignments`**: Assigns a platform-managed wholesale cost card to a reseller organization. Resellers cannot sell below rates defined in this cost card.

---

## 3. Financial Precision & Truncation Standard

To avoid rounding discrepancies, cumulative drift, and floating-point errors:
1. **Never Float**: Floating-point types (`float32`, `float64`, `DOUBLE PRECISION`) are strictly forbidden across the stack.
2. **PostgreSQL**: Stored as `NUMERIC(18,6)` in all database tables and views.
3. **Go Backend**: Handled exclusively via `github.com/shopspring/decimal.Decimal`.
4. **API & TypeScript**: Transferred and displayed as precision strings (e.g. `"0.007500"`).
5. **Truncation Rule**: All monetary computations truncate at 6 decimal places (`decimal.Truncate(6)`) rather than rounding up, preventing fractional cent inflation.

---

## 4. Effective Dating & Lifecycle

Rate adjustments follow strict temporal rules:
- **Half-Open Interval**: Valid when `effective_from <= at < effective_to`.
- **Zero-Overlap Invariant**: No two rates for the same `(card_id, country_id, operator_id, message_type)` may have overlapping temporal intervals.
- **Auto-Closing**: Publishing a new rate automatically sets `effective_to = new_rate.effective_from` on any active predecessor covering the same destination.
- **Upcoming Changes Window**: Future scheduled rates (`effective_from > now()`) are queried through `/api/v1/rates/upcoming?days=30` allowing customers and carriers 7, 14, or 30 days notice of price increases.

---

## 5. Specificity & Resolution Hierarchy

When rating an outbound SMS dispatched to a phone number:
1. **Customer Resolution**: Engine looks up the active `customer_rate_assignments` for the tenant at the message timestamp.
2. **Number Normalization**: Phone number is parsed and validated against E.164 rules.
3. **Prefix Matching**: In-memory prefix trie resolves the longest matching prefix to:
   - Destination `Country ID`
   - Destination `Operator ID` (if MCC/MNC prefix matched)
4. **Destination Rate Lookup Hierarchy**:
   - **Step 1 (Exact Match)**: Look for active rate with `(country_id, operator_id, message_type)`.
   - **Step 2 (Default Message Type)**: Look for active rate with `(country_id, operator_id, 'default')`.
   - **Step 3 (Country Fallback)**: Look for active rate with `(country_id, NULL, message_type)`.
   - **Step 4 (Country Default Fallback)**: Look for active rate with `(country_id, NULL, 'default')`.
5. **Temporal Filter**: The matched rate must satisfy `effective_from <= at < effective_to`.

---

## 6. Real-Time Margin Protection & Deficit Alerts

To protect gateway profitability:
1. **Upstream Buy Benchmark**: When creating or modifying a customer sell rate, the engine queries the lowest active `provider_buy` cost across all active upstream suppliers in the same currency.
2. **Deficit Check**: If `sell_price < lowest_buy_cost`, the engine flags a margin violation.
3. **Reseller Wholesale Floor**: If the seller is a reseller, `sell_price` must also be `>= reseller_cost_rate`. Reseller floor violations are strictly non-overridable.
4. **Override Governance**: Platform operators with `rate.margin.override` permission can explicitly authorize a negative margin rate (e.g., loss-leader agreements) by submitting an audited override reason.
5. **Live UI Indicator**: The frontend rate creation dialog provides real-time debounced feedback as prices are typed, showing margin percentage and highlighting deficits in red before form submission.
6. **Margin & Deficit Audit Report**: A dedicated dashboard at `/rates/margin-report` allows risk managers to audit profit margins across all active rate sheets and export violation reports to CSV.

---

## 7. Ultra-Fast In-Memory Engine Architecture

While transactional modifications occur in PostgreSQL, high-volume routing operations require microsecond latency:
- **`InMemoryEngine`**: Maintains an optimized in-memory index guarded by `sync.RWMutex`.
- **Trie Integration**: References `*number.Service` for concurrent O(k) E.164 phone number prefix lookups.
- **Snapshot Reloading**: Any change to rates (creation, update, close, rollback, or assignment) asynchronously triggers an engine reload.
- **Performance**:
  - In-memory rate resolution: **~723 ns/op** (0 heap allocations).
  - Streaming CSV import: **>1,290,000 rows/sec**.

---

## 8. Dual-Control Maker-Checker Workflow

All sensitive financial operations enforce separation of duties:
1. **Maker**: Submits a rate card, destination rate, or assignment change.
2. **Pending Change Request**: A record is created in `approval_requests` with diff snapshots.
3. **Checker**: A second authorized operator reviews the proposed change.
4. **Prevention of Self-Approval**: If the maker attempts to approve their own request, the system rejects it with `403 Forbidden` (`ErrMakerCannotApproveOwnChange`).
5. **Atomic Execution**: On approval, the service commits the database transaction and reloads the in-memory engine.

---

## 9. Immutable Audit Trail & Rollback

Every entity modification records an append-only audit log:
- `rate_cards_history`: Full state at each version.
- `rates_history`: Historical price, effective dates, and reasons.
- `customer_rate_assignments_history`: Past tenant assignments.
- `reseller_cost_assignments_history`: Past reseller wholesale cost sheets.

Rollback restores an entity to an exact previous version while validating optimistic concurrency (`version` match) to prevent stomping concurrent edits.

---

## 10. API Endpoint Reference

| Method | Path | Required Permission | Description |
|---|---|---|---|
| `GET` | `/api/v1/rates/cards` | `rate.buycard.read` / `rate.sellcard.read` | List rate cards with filters |
| `POST`| `/api/v1/rates/cards` | `rate.buycard.create` / `rate.sellcard.create` | Create a new rate card |
| `GET` | `/api/v1/rates/cards/:id` | `rate.buycard.read` / `rate.sellcard.read` | Get rate card details |
| `PUT` | `/api/v1/rates/cards/:id` | `rate.buycard.update` / `rate.sellcard.update` | Update rate card metadata |
| `POST`| `/api/v1/rates/cards/:id/archive` | `rate.buycard.update` / `rate.sellcard.update` | Archive a rate card |
| `GET` | `/api/v1/rates/cards/:id/history` | `rate.buycard.read` / `rate.sellcard.read` | View version snapshot history |
| `POST`| `/api/v1/rates/cards/:id/rollback`| `rate.buycard.update` / `rate.sellcard.update` | Rollback card to past version |
| `GET` | `/api/v1/rates` | `rate.buyrate.read` / `rate.sellrate.read` | List destination rates in card |
| `POST`| `/api/v1/rates` | `rate.buyrate.create` / `rate.sellrate.create` | Add new destination rate |
| `GET` | `/api/v1/rates/:id` | `rate.buyrate.read` / `rate.sellrate.read` | Get single rate details |
| `PUT` | `/api/v1/rates/:id` | `rate.buyrate.update` / `rate.sellrate.update` | Update rate price / interval |
| `POST`| `/api/v1/rates/:id/close` | `rate.buyrate.update` / `rate.sellrate.update` | Close an active rate as of now |
| `GET` | `/api/v1/rates/:id/history` | `rate.buyrate.read` / `rate.sellrate.read` | View rate version history |
| `POST`| `/api/v1/rates/:id/rollback`| `rate.buyrate.update` / `rate.sellrate.update` | Rollback rate to past version |
| `GET` | `/api/v1/rates/export` | `rate.buyrate.read` / `rate.sellrate.read` | Export rate sheet to CSV or JSON |
| `GET` | `/api/v1/rates/upcoming` | Authenticated | List scheduled future rate changes |
| `GET` | `/api/v1/rates/margin-check` | Authenticated | Real-time margin validation endpoint |
| `GET` | `/api/v1/rates/margin-report` | `rate.margin.view` | Margin & Deficit analysis report |
| `GET` | `/api/v1/rates/lookup` | Authenticated | Rating engine destination query |
| `GET` | `/api/v1/rates/lookup-by-number` | Authenticated | Rating engine E.164 phone lookup |
| `POST`| `/api/v1/rates/lookup/bulk` | Authenticated | Batch phone number rate calculator |
| `GET` | `/api/v1/rates/assignments` | `rate.assignment.read` | List customer & reseller assignments |
| `POST`| `/api/v1/rates/assignments/customer` | `rate.assignment.create` | Assign sell card to customer |
| `POST`| `/api/v1/rates/assignments/reseller` | `rate.assignment.create` | Assign wholesale cost to reseller |
| `POST`| `/api/v1/rates/assignments/:id/deactivate` | `rate.assignment.update` | Deactivate card assignment |
| `GET` | `/api/v1/rates/my-rates` | Authenticated (Customer) | View own active SMS rate sheet |
| `GET` | `/api/v1/rates/my-cost` | Authenticated (Reseller) | View own assigned wholesale cost sheet |

---

## 11. Frontend Route Directory

The frontend provides 8 specialized views under `frontend/src/routes/(app)/rates`:

1. `/rates/buy-cards` — Upstream wholesale provider buy cards list, create modal, and audit history.
2. `/rates/buy-cards/[id]` — Detailed buy rate table with live pagination, CSV export, and rate editor.
3. `/rates/sell-cards` — Customer sell cards management.
4. `/rates/sell-cards/[id]` — Sell rate sheet with integrated real-time margin check alerts and deficit override warnings.
5. `/rates/assignments` — Customer rate card assignments and Reseller wholesale cost assignments.
6. `/rates/margin-report` — Profit/loss margin analysis dashboard with violation filters and CSV export.
7. `/rates/lookup` — Interactive rate calculator supporting both E.164 phone numbers and destination country/operator selections.
8. `/rates/upcoming` — Schedule of future rate changes with countdown badges.
9. `/rates/my-cost` — Tenant view for resellers to inspect their wholesale cost floor.
10. `/rates/my-rates` — Tenant view for customers to inspect their active SMS pricing.

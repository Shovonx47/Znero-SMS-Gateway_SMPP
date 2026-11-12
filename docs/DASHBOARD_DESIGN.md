# Znero International — Phase 9 Visibility & Analytics Architecture Design (`docs/DASHBOARD_DESIGN.md`)

## 1. Executive Summary & Purpose

Phase 9 ("Visibility") provides real-time NOC dashboards, executive revenue/margin analytics, reseller and customer visibility portals, alert evaluation, operational insights, settings management, search, and scheduled reports. 

This document defines the mathematical foundations, data models, event-time semantics, PostgreSQL rollup architecture, and tenant isolation guarantees governing all Phase 9 visibility components.

---

## 2. Source of Truth & Financial Definitions

### 2.1 Ledger as the Sole Authority on Revenue
- **Realized Revenue**: Money actually captured in the double-entry journal (`ledger_transactions` with `type = 'hold_capture'`). 
  - Revenue is credited to the platform revenue account (`PLATFORM_REVENUE` or `PLATFORM_REVENUE_EUR`) and debited from the customer's wallet account.
  - Linkage: Every capture transaction specifies `reference_type = 'message'` and `reference_id = message.id`. Furthermore, the originating hold (`holds`) specifies `reference_type = 'message'`, `reference_id = message.id`, and `wallet_id = wallet.id`.
  - In `messages`, upon final settlement transition (`TransitionToFinalStateTx`), the captured amount is recorded atomically.
  - Zero double-counting: Historical reconciliation continuously verifies that the sum of captured entries matches `messages.sell_price` for delivered/accepted messages.
- **Pending / Unrealized Liabilities (Active Holds)**:
  - Balances currently locked in `holds` with `status = 'active'`.
  - Active holds are explicitly reported as "Pending Holds / Unrealized Exposure" and are **NEVER** blended into Revenue or Gross Profit.
- **Estimated Buy Cost**:
  - Provider payables and upstream invoices are out of scope. Cost is tracked as the buy cost recorded on attempts where the carrier accepted the message (`message_attempts` with `status = 'accepted'`), utilizing `message.actual_buy_cost` or `estimated_buy_cost`.
- **Gross Margin & Profitability**:
  - `Margin = Realized Revenue - Estimated Buy Cost`.
  - **Strict Currency Isolation**: Margin is computed **ONLY** where the revenue currency and the buy cost currency are identical. If currencies differ (e.g. customer billed in EUR, carrier invoiced in USD), margin is explicitly designated as `not_comparable` (no synthetic FX conversion).

### 2.2 Operational Quality Metrics
- **Delivery Rate (ASR / DLR Ratio)**:
  $$\text{Delivery Rate} = \frac{\text{Count}(\text{status} = \text{delivered})}{\text{Count}(\text{status} \in \{\text{delivered}, \text{undelivered}, \text{expired}, \text{failed}, \text{rejected}\})}$$
  (Messages in transient states `accepted`, `queued`, `submitting`, and `unknown` are in-flight and excluded from the denominator to avoid distorting real-time ratios).
- **Unknown Rate & Exposure**:
  - `Count(status = 'unknown')` and sum of associated active hold amounts awaiting DLR reaper timeout.
- **Latency Histograms (Fixed Buckets)**:
  - Fixed logarithmic/exponential latency buckets:
    `[0, 50ms), [50ms, 100ms), [100ms, 250ms), [250ms, 500ms), [500ms, 1s), [1s, 2s), [2s, 5s), [5s, 10s), [10s, 30s), [30s, +inf)`.
  - Latency stages tracked:
    1. **Ingress-to-Carrier Accept** (`attempt.finished_at - message.created_at`)
    2. **Carrier-to-DLR Lag** (`dlr.created_at - attempt.finished_at`)
    3. **End-to-End Latency** (`dlr.created_at - message.created_at`)

---

## 3. Event-Time Semantics

### 3.1 Principle of Event-Time Attribution
All analytical rollups are bucketed by the **time the event occurred**, NOT message creation time:
- Message Submission $\rightarrow$ bucketed at `message.created_at`.
- Provider Acceptance $\rightarrow$ bucketed at `message_attempt.finished_at`.
- Delivery Confirmation $\rightarrow$ bucketed at `message_dlr.created_at` or `message_event.created_at`.
- Financial Settlement $\rightarrow$ bucketed at `ledger_transactions.created_at`.

### 3.2 Guarantee of Historical Immutability
Under message creation-time bucketing, a DLR arriving 24 hours late would require rewriting yesterday's closed database aggregates, invalidating cached reports and auditing. Under event-time bucketing, yesterday's closed buckets remain **immutable**. The late DLR simply increments the delivery count and revenue capture in today's current 15-minute bucket.

---

## 4. PostgreSQL Rollup Architecture (No ClickHouse)

### 4.1 15-Minute Base Buckets for Global Timezone Realignment
- PostgreSQL time-bucket function:
  $$\text{bucket\_start} = \text{date\_trunc}('hour', ts) + \lfloor\text{date\_part}('minute', ts) / 15\rfloor \times \text{interval } '15 \text{ minutes}'$$
- **Global Timezone Compatibility**:
  - While 1-hour buckets fail for half-hour and 45-minute timezones (e.g. `Asia/Kolkata` at `UTC+5:30`, `Asia/Kathmandu` at `UTC+5:45`), 15 minutes is a common divisor of 60, 30, and 45.
  - Any user can group 15-minute buckets into their local calendar days ($D_{\text{local}} = [00:00, 24:00)_{\text{tz}}$) without data loss or phase errors.

### 4.2 Dimension Fact Table Decomposition
A unified fact table covering all dimensions would result in $1,000 \times 20 \times 200 \times 4 \times 96 \times 90 \approx 138 \times 10^9$ potential cells. To guarantee query budgets under 300ms, data is split into three orthogonal tables:

1. `analytics_rollup_customer_15m`:
   - Dimensions: `(bucket_start, customer_org_id, currency, channel, ingress_channel, billing_mode)`
   - Metrics: `submitted_count`, `delivered_count`, `undelivered_count`, `failed_count`, `expired_count`, `revenue_amount`, `holds_amount`, `latency_histogram`
   - Max size (1,000 customers $\times$ 96 buckets/day $\times$ 90 days): $\approx 8.6\times 10^6$ rows.
2. `analytics_rollup_provider_15m` (Platform-Only):
   - Dimensions: `(bucket_start, provider_connection_id, currency, route_plan_id)`
   - Metrics: `attempt_count`, `accepted_count`, `rejected_count`, `estimated_buy_cost`, `latency_histogram`
   - Max size (20 connections $\times$ 96 buckets/day $\times$ 90 days): $\approx 1.7\times 10^5$ rows.
3. `analytics_rollup_destination_15m`:
   - Dimensions: `(bucket_start, customer_org_id, country_id, operator_id)`
   - Metrics: `submitted_count`, `delivered_count`, `failed_count`
   - Sparse table: active traffic routes only ($\approx 5\times 10^5$ rows).

### 4.3 Partitioning & Retention
- Tables partitioned monthly (`PARTITION BY RANGE (bucket_start)`).
- Automated retention worker drops partitions older than 90 days.

---

## 5. Incremental Consumption & Exactly-Once Durable Cursors

### 5.1 Append-Only Source Streams
The rollup worker consumes from three append-only streams:
1. `message_events` (lifecycle status changes)
2. `ledger_transactions` (revenue captures & refunds)
3. `message_attempts` (provider dispatch outcomes & buy costs)

### 5.2 Transactional High-Water Mark (`analytics_cursors`)
- Schema: `analytics_cursors (cursor_name VARCHAR PRIMARY KEY, last_event_id UUID, last_event_time TIMESTAMPTZ, updated_at TIMESTAMPTZ)`
- **Atomic Commits**:
  ```sql
  BEGIN;
  -- 1. Read batch from source stream WHERE id > last_event_id ORDER BY id ASC LIMIT 5000
  -- 2. Upsert increments into analytics_rollup_*_15m
  -- 3. UPDATE analytics_cursors SET last_event_id = :max_id, last_event_time = :max_ts;
  COMMIT;
  ```
- **Crash Invariant**: If the process crashes mid-batch, PostgreSQL automatically rolls back both the rollup upserts and the cursor update. Upon restart, processing resumes from the exact committed high-water mark with **zero double counting**.

---

## 6. Real-Time NOC & Live Hub Architecture

### 6.1 Dual-Tier Latency Model
- Historical analytics: Sourced from 15-minute rollups (lag: 10–30s).
- Real-time NOC telemetry: Sourced from narrow, indexed rolling 60-second window queries and heartbeat tables:
  - Ingress TPS: `SELECT COUNT(*) FROM message_locator WHERE created_at >= now() - interval '60 seconds'`
  - Outbox Depth: `SELECT count(*), coalesce(extract(epoch from now() - min(created_at)), 0) FROM message_outbox WHERE status = 'pending'`
  - Provider Connection State: Polled from `provider_connections` and in-memory health monitors.

### 6.2 Flat Query Cost via Shared Broadcast Hub
- The `api` process maintains an in-memory 2-second telemetry cache.
- Regardless of whether 1 or 500 WebSocket clients are connected, the database is queried at most once every 2 seconds. The Live Hub fans out JSON frames over WebSocket connections without increasing DB connection load.

---

## 7. Multi-Tenant Visibility & Projection Boundary

### 7.1 Audience Segmentation Rules
| Field / Dimension | Platform Admin | Reseller Admin | Customer Admin |
|---|---|---|---|
| Aggregated Message Volumes | All Orgs | Reseller + Sub-tree | Own Org Only |
| Realized Revenue | Total Platform | Reseller Tree Total | Customer Spend |
| Buy Cost & Supplier Invoices | Full Access | **STRICTLY STRIPPED (403)** | **STRICTLY STRIPPED (403)** |
| Gross Margin / Profit | Full Access | **STRICTLY STRIPPED (403)** | **STRICTLY STRIPPED (403)** |
| Provider Identity / Connection ID | Full Access | **STRICTLY STRIPPED (403)** | **STRICTLY STRIPPED (403)** |
| Internal Routing Plan / Rules | Full Access | **STRICTLY STRIPPED (403)** | **STRICTLY STRIPPED (403)** |
| Recipient MSISDN | Masked (+1202***0199) | Masked (+1202***0199) | Masked (+1202***0199) |
| Message Content / PDU | Content Perm Only | Content Perm Only | Content Perm Only |

### 7.2 Compile-Time Query Projection
Non-platform queries are not filtered in Go memory after fetching. Instead, the SQL query generator uses separate projection builders (`buildPlatformSelect()` vs `buildCustomerSelect()`). Commercial carrier fields are never included in the SQL `SELECT` list for non-platform users, preventing SQL injection or memory leak exposures.

---

## 8. Performance Budgets & SLA Guarantees

- **Executive Dashboard Summary Query**: $p95 < 300\text{ms}$ over 90 days and 1M messages.
- **NOC Live Telemetry Stream**: $p95 < 100\text{ms}$ refresh cycle.
- **Rollup Processing Lag**: Under 60 seconds from event arrival to rollup table visibility.
- **Zero Impact on Message Intake**: Rollup transactions run at `READ COMMITTED` or `REPEATABLE READ READ ONLY` against historical logs. They never acquire row-level locks on `messages`, `holds`, or `wallets`.


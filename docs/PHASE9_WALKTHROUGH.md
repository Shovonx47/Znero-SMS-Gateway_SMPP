# Phase 9: Visibility & Analytics Walkthrough Guide

This document provides a guided walkthrough of the **Phase 9 Visibility Stack** in **Znero International**, demonstrating high-throughput wholesale SMS dashboards, real-time NOC telemetry, multi-tenant isolation, alerting, incident management, settings registry, and asynchronous reporting.

---

## 1. Quick Start Demo Stack

A dedicated demo database (`znero_demo`) is pre-provisioned with 30 days of multi-currency SMS traffic, pre-computed rollups, carrier connections, active firing alerts, incidents, and ready-to-download export files.

To start the demo stack in one command:

```powershell
.\scripts\demo-up.ps1
```

To stop all background demo processes cleanly:

```powershell
.\scripts\demo-down.ps1
```

### Access URLs
- **Web UI Portal**: [http://127.0.0.1:5173](http://127.0.0.1:5173)
- **API Server**: [http://127.0.0.1:8080](http://127.0.0.1:8080)
- **Live Hub WebSocket**: `ws://127.0.0.1:8080/api/v1/ws/live`

---

## 2. Demo User Personas & Credentials

The demo database features strict 3-tier multi-tenancy. You can log into any of the following accounts:

| Persona | Email | Password | Access Boundary & Scope |
| :--- | :--- | :--- | :--- |
| **Platform Super Admin** | `admin@znero.internal` | `AdminSecret123!` | Full platform visibility: upstream carrier names, wholesale buy costs, profit margins, NOC live TPS meter, system settings. |
| **NOC Operator** | `operator@znero.internal` | `NocSecret123!` | NOC real-time telemetry, carrier connections matrix, incident management. |
| **Reseller Admin** | `admin@apextelecom.net` | `ResellerSecret123!` | Subtree accounts only (`Apex Global Telecom`). Sees child customer usage and revenue. **Zero** upstream provider names or buy costs exposed. |
| **Customer Admin** | `dev@fintechpay.com` | `CustomerSecret123!` | Leaf tenant only (`Fintech Pay SMS`). Sees personal delivery rates, customer spend, and DLR error diagnostics. |

---

## 3. Guided Feature Tour

### Tour 1: Executive Dashboard (`/analytics`)
1. Log in as **Platform Super Admin** (`admin@znero.internal`).
2. Navigate to **Analytics > Executive Overview** in the sidebar.
3. Observe:
   - **KPI Cards**: Total Submitted messages, Delivery Rate (%), Gross Revenue per currency (USD, EUR, GBP, BDT), and Platform Net Margin.
   - **Time Series Trend Chart**: Interactive hourly message volume and delivery rate histogram with tooltip diagnostics.
   - **Top Customer Accounts Table**: Ranked list of highest volume clients.
   - **Upstream Carrier Performance Matrix**: Carrier delivery rate, p95 latency, and direct supplier cost (strictly hidden from resellers and customers).
4. Use the **Date Range Selector** (`Today`, `Last 7 Days`, `Last 30 Days`) to observe dynamic interval aggregation.

### Tour 2: Real-time NOC & Live TPS Meter (`/analytics/noc`)
1. Navigate to **Analytics > NOC & Live TPS**.
2. Notice the **Live Connection Pill**:
   - When connected via WebSocket, displays `Live Real-time WebSocket` with a pulsing emerald dot.
   - If the WebSocket closes, automatically transitions to `5s Polling Fallback` with zero disruption.
3. Observe:
   - **Live TPS Gauge**: Visual throughput meter tracking switching load in real time.
   - **Platform Health Status**: `Healthy`, `Degraded`, or `Critical` based on active firing alert conditions.
   - **Backlog Queue Depth**: Real-time count of messages in transit.
   - **Active Carrier Connections Matrix**: Upstream SMPP and HTTP connections (e.g. `Vodafone Primary TX`, `Tata Tier-1 TX`, `Twilio Carrier API`) displaying live TPS, round-trip latency, queue depth, and error rate.
   - **Firing Alerts Ticker**: Active notifications alerting NOC engineers of carrier degradation.

### Tour 3: Reseller Analytics & Tenancy Isolation (`/analytics/reseller`)
1. Log out and sign in as **Reseller Admin** (`admin@apextelecom.net`).
2. Navigate to **Analytics > Reseller Analytics**.
3. Verify Tenancy Guardrails:
   - Reseller sees only their child accounts (`Fintech Pay SMS`, `HealthAlert Notifications`).
   - Upstream carrier names (Vodafone, Tata, Twilio) and buy costs are **completely stripped** from all API responses and UI tables.
   - Multi-currency revenue is displayed per currency without unsafe currency conversion.

### Tour 4: Customer Analytics & Error Diagnostics (`/analytics/customer`)
1. Sign in as **Customer Admin** (`dev@fintechpay.com`).
2. Navigate to **Analytics > Customer Analytics**.
3. Observe:
   - Total messages submitted and delivered.
   - Total customer spend per currency.
   - **Delivery Failure Diagnostics Table**: Breakdown of DLR failure error codes (e.g., `UNDELIV`, `EXPIRED`, `REJECTED`) with count and failure percentage.

### Tour 5: Alert Rules & Silence Controls (`/alerts`)
1. Sign back in as **Platform Super Admin**.
2. Navigate to **Alert Rules**.
3. Observe the catalog of active threshold rules:
   - `Provider Connection Latency Spike` (`latency_p95_ms > 150`)
   - `Tata Tier-1 DLR Failure Rate Exceeded` (`dlr_failure_rate > 0.15` — currently FIRING)
   - `Platform Outbound Queue Surge` (`queue_depth > 5000`)
   - `Gross Margin Compression` (`gross_margin < 0.08`)
4. Click **Silence** on any rule to configure a temporary suppression window (15m, 1h, 4h, 24h).

### Tour 6: Incident Management Center (`/incidents`)
1. Navigate to **Incident Center**.
2. Review active and resolved platform incidents:
   - `Upstream Carrier Route Flapping on Tata Tier-1` (`INVESTIGATING`, Severity: High).
   - `Customer Webhook Delivery Transient Backpressure` (`RESOLVED`, Severity: Medium).
3. Click an incident to open the details view:
   - Use the **Workflow State Buttons** (`open` -> `investigating` -> `identified` -> `monitoring` -> `resolved`) to update status.
   - Use the **Append Timeline Update** form to log investigation actions or mitigation steps.
4. Click **Declare Incident** to report a new operational outage.

### Tour 7: Settings Registry (`/settings`)
1. Navigate to **Settings Center**.
2. Browse category tabs: `Platform`, `Billing`, `Routing`, `Alerts`.
3. Test typed editing:
   - Integer/Float fields reject non-numeric input.
   - Boolean fields render interactive toggle switches.
   - JSON fields validate syntax before saving.
   - Secret keys (e.g. API keys, provider credentials) are masked by default with a **Show/Hide** toggle.
4. Click **Factory Reset** to safely revert any setting to its schema default.

### Tour 8: Asynchronous Export Center (`/exports`)
1. Navigate to **Export Center**.
2. Notice the pre-generated export jobs ready for instant download:
   - Monthly Message Log (CSV format)
   - Quarterly Delivery Audit (Excel XLSX format)
   - Financial Margin Reconciliation (JSON format)
3. Click **Download** on any completed job to download the sanitized report.
4. Test formula injection protection:
   - Cells containing `=`, `+`, `-`, `@`, or leading whitespace are automatically escaped with `'` to prevent DDE/Excel formula execution.
5. Submit a new export job using the top form:
   - Select Report Type (`messages`, `delivery_report`, `financial_report`).
   - Select Format (`csv`, `xlsx`, `json`).
   - Click **Queue Export Job**.
   - The job immediately appears as `processing` and transitions to `completed` within seconds via the asynchronous background worker.

### Tour 9: Bilingual Localization (i18n) & Dark Mode
1. In the top navigation bar, click the **বাংলা / EN** button:
   - Instant live transition between English and Bengali across all page titles, metrics, navigation items, and action buttons.
   - Locale preference is persisted across browser refreshes in `localStorage`.
2. Click the **Sun / Moon** icon in the topbar:
   - Smoothly toggles between Dark Mode and Light Mode with high-contrast UI accents.

---

## 4. Verification & Audit Summary

- **Routes Audited**: 435 unique HTTP endpoints verified under `TestAuthzMatrix_AllRoutesAcrossFivePrincipals` with 0 bypasses.
- **Linters**:
  - `golangci-lint`: 0 issues
  - `gofmt`: 100% formatted
  - `svelte-check`: 0 errors, 0 warnings
  - `tsc --noEmit`: 0 TypeScript errors
  - Production build: `vite build` completed in 17.50s.

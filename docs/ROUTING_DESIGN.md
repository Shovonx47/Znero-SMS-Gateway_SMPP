# Znero International — Routing Engine Design & Architecture (Phase 7)

## 1. Architectural Principles

1. **Decision Engine Only**: Phase 7 is strictly an algorithmic decision engine. It does not establish outbound network connections, bind SMPP sessions, enqueue messages, or process DLR receipts. Phase 8 (SMPP / HTTP Egress) invokes this engine to determine the target provider connection for each outbound message.
2. **Deterministic & Explainable**: Given an identical snapshot and input parameters, the decision engine produces an identical result. Every decision yields an explainable trace specifying evaluated plans, triggered rules, ranked candidates, and explicit disqualification reasons for rejected connections.
3. **Zero Database Queries in Hot Path**: The routing engine executes entirely from an immutable in-memory snapshot (`sync/atomic.Pointer[RoutingSnapshot]`). All route plans, entries, customer assignments, routing rules, coverage records, active rates, and kill switch overrides reside in memory. Resolving a route incurs zero SQL queries and zero lock contention on the read path.
4. **Exact Decimal Arithmetic**: All monetary rates, margins, and cost comparisons are calculated using fixed-scale decimal arithmetic (`NUMERIC(18, 4)` in PostgreSQL, `shopspring/decimal` in Go). Floating-point arithmetic (`float32`, `float64`) is strictly prohibited.
5. **Platform-Only Scope**: Upstream providers, egress connections, buy rates, margins, and route decisions are proprietary to the platform operator. Reseller and customer organizations receive `403 Forbidden` on all routing endpoints and cannot view routing navigation.

---

## 2. Decision Pipeline Lifecycle

```
[Inbound Request]
       │
       ▼
[0. Normalization] ───► Preserves raw destination & normalizes E.164 via Prefix Trie
       │
       ▼
[1. Routing Rules] ───► Evaluates high-priority overrides (Block, Force, Avoid, Prefer)
       │                 • If "Block" ──► Return immediately with block reason code
       │                 • If "Force" ──► Pins connection as priority #1 if eligible
       ▼
[2. Plan Selection] ──► Customer country override > Customer default plan
       │                 • If no active plan ──► Disqualified with "no_plan"
       ▼
[3. Hard Eligibility] ─► Evaluates each plan entry against 7 non-negotiable gates:
       │                 1. Entry is active
       │                 2. Connection is active (status = active)
       │                 3. Provider is active (status = active)
       │                 4. Connection is NOT kill-switched
       │                 5. Destination country/operator is covered
       │                 6. Sender ID type is allowed on connection
       │                 7. Active buy rate exists and margin satisfies threshold
       ▼
[4. Strategy Ranking] ─► Sorts eligible candidates according to plan strategy:
       │                 • Priority: Ascending priority rank (1 = primary)
       │                 • Least Cost: Ascending buy rate (cheapest first)
       │                 • Weighted Split: Cumulative weight probability distribution
       ▼
[5. Failover Support] ─► Supports Next(req, excludedConns) for retry sequences
```

---

## 3. Hard Eligibility Checklist

Before any candidate connection can be considered for strategy ranking, it must satisfy all 7 eligibility criteria:

| Gate | Check | Exclusion Code | Action on Failure |
| :--- | :--- | :--- | :--- |
| **1. Entry Status** | `entry.Status == 'active'` | `entry_disabled` | Disqualified from candidate pool |
| **2. Override / Kill Switch** | `connection_id NOT IN overrides` | `kill_switched` | Instantly disqualified |
| **3. Connection Status** | `connection.Status == 'active'` | `connection_inactive` | Disqualified |
| **4. Provider Status** | `provider.Status == 'active'` | `provider_inactive` | Disqualified |
| **5. Destination Coverage** | `(country, operator) IN provider_coverage` | `no_coverage` | Disqualified |
| **6. Sender ID Support** | `sender_id_type IN entry.allowed_sender_id_types` | `sender_id_unsupported` | Disqualified |
| **7. Economics & Rates** | Active buy rate exists; margin $\ge$ min threshold | `no_buy_rate` / `negative_margin` | Disqualified |

---

## 4. In-Memory Snapshot & Background Reload

### 4.1 Lock-Free Snapshot (`sync/atomic.Pointer`)
The engine maintains a global atomic pointer:
```go
var currentSnapshot atomic.Pointer[RoutingSnapshot]
```
Resolving a route requires only:
```go
snap := currentSnapshot.Load()
```
This enables concurrent goroutines to execute millions of resolutions per second with zero mutex locking.

### 4.2 Safe Atomic Reload
- A background worker reloads the snapshot every 60 seconds (`TryReload()`).
- Database mutations (plan activation, rule change, kill switch activation) trigger an immediate synchronous or reactive snapshot reload.
- **Fail-Safe Mechanism**: If a database error or network partition occurs during snapshot construction, `TryReload()` logs an error and retains the **last-known-good snapshot** untouched. The engine never wipes active memory on a failed query.

---

## 5. Emergency Kill Switch

- Provides a circuit-breaker to instantly take down an erratic or degraded provider connection.
- **Immediate Effect**: Inserting an override into `provider_connection_overrides` triggers an immediate atomic snapshot reload. All subsequent `Resolve()` calls disqualify the connection within microseconds.
- **Audit Requirement**: Activating the kill switch requires a mandatory `reason` text stored permanently in audit logs.
- **Dual-Control Recovery**: Re-enabling a killed connection is classified as a sensitive operation that routes through the Maker-Checker approval queue (`connection_reenable`), preventing unilateral re-activation of flawed upstream links.

---

## 6. Phase 8 Handoff Contract

Phase 8 SMS switching workers invoke `internal/routing` using the following Go contract:

```go
type Router interface {
    // Resolve calculates the optimal provider connection for an outbound message.
    Resolve(ctx context.Context, req ResolveRequest) (*ResolveResult, error)

    // Next returns the next fallback candidate excluding previously failed connections.
    Next(ctx context.Context, req ResolveRequest, failedConnectionIDs []uuid.UUID) (*Candidate, error)

    // Simulate runs Resolve with explainable pipeline traces and audit logging.
    Simulate(ctx context.Context, tc *tenant.Context, req SimulateRequest) (*SimulateResult, error)
}
```
If `Resolve` returns `len(res.Candidates) == 0`, delivery is declared impossible and the message fails fast with an explicit disqualification code (`no_coverage`, `kill_switched`, `no_plan`, etc.) without wasting outbound network attempts.

---

## 7. Performance Benchmark Specification

### 7.1 Throughput & Latency Target
The routing decision engine achieves **12.1 – 12.35 µs per decision** in microbenchmarks (`BenchmarkResolve_RealisticDataset`), translating to over **80,000 decisions/second per CPU core**.

### 7.2 Benchmark Dataset & Environment
- **Dataset Scale**: 50 complete route plans, each configured with 10 candidate egress connections (500 total entries), full provider coverage graphs, customer route assignments with country-specific overrides, and active rule sets.
- **Hardware Profile**: Standard single-core execution on x86_64 CPU (e.g. Intel Core i5-8265U @ 1.60GHz base).
- **Zero I/O Guarantee**: All operations execute against an immutable in-memory snapshot (`atomic.Pointer[RoutingSnapshot]`) with zero database queries, zero network calls, and zero mutex contention.

### 7.3 Full Pipeline Execution Scope
Within each 12 µs invocation, the engine executes the complete routing pipeline:
1. Destination E.164 normalization & validation.
2. Customer profile status, allowed destination country policy, and approved sender ID checks.
3. Plan resolution by priority and customer country assignment override.
4. Routing rule matching and evaluation (`block`, `force`, `prefer`, `avoid`).
5. Comprehensive entry eligibility evaluation:
   - Route plan entry active status.
   - Provider connection and provider entity active statuses.
   - Provider connection kill-switch override check.
   - Provider country & operator coverage validation (including "tested" coverage gating).
   - Sender ID type gating (`numeric`, `alphanumeric`).
   - Dynamic buy and sell rate lookup with effective date-window validation.
   - Exact decimal margin threshold check (`shopspring/decimal` / `NUMERIC(18,6)`).
6. Strategy candidate sorting (Priority rank, Least Cost with exact decimal tie-breaking, or Weighted split).
7. Fallback list generation with 1-based ranks (`primary`, `failover`).

---

## 8. Permissions Hierarchy & Dual-Control Governance

### 8.1 Permission Scoping: Platform vs Entity Override (`routing.override` vs `routing.entry.override`)
- **`routing.override`**:
  - **Who Holds It**: Exclusively **Platform Super Admin** (`Super Admin` / `Platform Admin`). Neither `Routing Maker`, `Routing Checker`, `Master Data Editor`, `Master Data Approver`, nor any reseller/customer role holds `routing.override`.
  - **What It Bypasses**: Bypasses Maker-Checker approval across **all** routing entities (`RequestPlanActivation`, `RequestPlanDeactivation`, `CreateEntry`/`UpdateEntry`/`DeleteEntry` on active plans, `CreateAssignment`/`UpdateAssignment`/`DeleteAssignment`, and `CreateRule`/`UpdateRule`/`DeleteRule`), applying changes directly in a single audited transaction. Note: `EnableConnection` (re-enabling a kill-switched connection) still requires Maker-Checker approval via `connection_reenable`.
- **`routing.entry.override`**:
  - **Who Holds It**: Exclusively **Platform Super Admin** (`Super Admin` / `Platform Admin`, or a custom senior routing engineer role explicitly granted by Super Admin). **Maker and Checker role templates (`Routing Maker`, `Routing Checker`) hold NEITHER `routing.override` NOR `routing.entry.override`.**
  - **What It Bypasses**: Bypasses Maker-Checker approval **only** for route plan entry mutations (`CreateEntry`, `UpdateEntry`, `DeleteEntry`) inside an active route plan. It does **not** bypass approval for plan activation/deactivation, customer route assignments, routing rules, or kill-switch re-enablement.

### 8.2 Dedicated Routing Role Templates & Separation of Duties (Maker-Checker)
Authoritative route plans, rules, and customer assignments govern telecommunications routing topology and financial margins. To enforce strict least privilege, `routing.*` permissions are separated from `Master Data Editor` and `Master Data Approver` into dedicated platform system role templates:
- **Routing Maker (`Routing Maker`)**:
  - **Permissions**: `routing.plan.view`, `routing.plan.create`, `routing.plan.edit`, `routing.plan.activate`, `routing.plan.deactivate`, `routing.plan.archive`, `routing.entry.view`, `routing.entry.create`, `routing.entry.edit`, `routing.assignment.view`, `routing.assignment.create`, `routing.assignment.edit`, `routing.rule.view`, `routing.rule.create`, `routing.rule.edit`, `routing.rule.activate`, `routing.simulate`, `routing.snapshot.view`, `provider.provider.view`, `provider.connection.view`, `customer.profile.view`, `country.country.view`, `operator.operator.view`.
  - **Explicitly Excluded**: Holds **zero** `*.approve` permissions, **zero** `routing.killswitch` permissions, and **neither** `routing.override` nor `routing.entry.override`.
- **Routing Checker (`Routing Checker`)**:
  - **Permissions**: `routing.plan.view`, `routing.plan.approve`, `routing.entry.view`, `routing.entry.approve`, `routing.assignment.view`, `routing.assignment.approve`, `routing.rule.view`, `routing.rule.approve`, `routing.rule.activate`, `routing.simulate`, `routing.snapshot.view`, `routing.killswitch`, `provider.provider.view`, `provider.connection.view`, `customer.profile.view`, `country.country.view`, `operator.operator.view`.
  - **Explicitly Excluded**: Holds **zero** `*.create` / `*.edit` permissions and **neither** `routing.override` nor `routing.entry.override`.
- **`Master Data Editor` & `Master Data Approver`**:
  - Hold **zero** `routing.*` permissions.
- **Enforcement Rules**:
  - **Self-Approval Prohibition**: The maker who created the change request cannot approve their own change (`ErrMakerCannotApproveOwnChange`), even if granted checker privileges.
  - **Mandatory Rejection Reason**: Rejecting a change request requires an explicit, audited explanation (`ErrEmptyRejectReason`).
  - **Kill Switch Lifecycle**: Disabling a degraded connection applies instantly with an audited reason; re-enabling that connection requires formal Maker-Checker approval to ensure upstream links are verified healthy before restoring live traffic.



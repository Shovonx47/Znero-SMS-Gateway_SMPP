# Znero International — RBAC Permission Matrix

This document defines the authoritative Role-Based Access Control (RBAC) matrix across Platform, Reseller, and Customer organization types, including the separation of duties between Maker and Checker roles and the exact behavior of override permissions.

---

## 1. Platform vs. Tenant Role Templates

| Role Template | Org Type | Purpose & Separation of Duties |
| :--- | :--- | :--- |
| **`Super Admin`** | `platform` | Full platform administrative authority, including `*.override` and `*.approve` permissions. |
| **`Master Data Editor`** | `platform` | **Maker** for Master Data, Providers, Customers, Sender IDs, and Rates. Holds **no** `*.approve`, **no** `*.override`, and **no** `routing.*` permissions. |
| **`Master Data Approver`** | `platform` | **Checker** for Master Data, Providers, Customers, Sender IDs, and Rates. Holds `*.view` and `*.approve` only; holds **no** `*.create`/`*.edit`, **no** `*.override`, and **no** `routing.*` permissions. |
| **`Routing Maker`** | `platform` | **Maker** dedicated to Phase 7 Routing (`routing.plan.*`, `routing.entry.*`, `routing.assignment.*`, `routing.rule.*`, `routing.simulate`, `routing.snapshot.view`). Holds **no** `*.approve`, **no** `routing.killswitch`, and **neither** `routing.override` nor `routing.entry.override`. |
| **`Routing Checker`** | `platform` | **Checker** dedicated to Phase 7 Routing (`routing.*.view`, `routing.*.approve`, `routing.simulate`, `routing.snapshot.view`, `routing.killswitch`). Holds **no** `*.create`/`*.edit` and **neither** `routing.override` nor `routing.entry.override`. |
| **`Org Admin`** | `platform` / `reseller` / `customer` | Tenant-scoped organization administrator. In `reseller` and `customer` orgs, platform-only namespaces (`provider.*`, `routing.*`, `rate.buy*`, `rate.margin.*`, `import.*`, master data writes) are strictly stripped. |
| **`Staff`** | `platform` / `reseller` / `customer` | View organization resources and invite users within tenant scope. |
| **`Read-only`** | `platform` / `reseller` / `customer` | Read-only (`*.view`) access within tenant scope. |

---

## 2. Routing Permissions Matrix (Phase 7 — Platform-Only)

All `routing.*` endpoints enforce `requirePlatformOrg` (`!tc.IsPlatformOrg()` returns `403 Forbidden`) in addition to the permission check below. Reseller and Customer roles hold **zero** `routing.*` permissions.

| Permission Code | Description | `Super Admin` | `Routing Maker` | `Routing Checker` | `Master Data Editor` | `Master Data Approver` | Reseller / Customer Roles |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| `routing.plan.view` | View route plans, history, and exports | Yes | Yes | Yes | No | No | No |
| `routing.plan.create` | Create draft route plans | Yes | Yes | No | No | No | No |
| `routing.plan.edit` | Edit or rollback route plans | Yes | Yes | No | No | No | No |
| `routing.plan.activate` | Request route plan activation | Yes | Yes | No | No | No | No |
| `routing.plan.deactivate` | Request route plan deactivation | Yes | Yes | No | No | No | No |
| `routing.plan.archive` | Archive deactivated/draft route plans | Yes | Yes | No | No | No | No |
| `routing.plan.approve` | Approve/reject route plan activation & deactivation | Yes | No | Yes | No | No | No |
| `routing.entry.view` | View route plan entries, history, and exports | Yes | Yes | Yes | No | No | No |
| `routing.entry.create` | Add entries to a route plan | Yes | Yes | No | No | No | No |
| `routing.entry.edit` | Edit, delete/remove, or rollback route plan entries | Yes | Yes | No | No | No | No |
| `routing.entry.approve` | Approve/reject entry changes on active route plans | Yes | No | Yes | No | No | No |
| `routing.assignment.view` | View customer route assignments, history, and exports | Yes | Yes | Yes | No | No | No |
| `routing.assignment.create` | Create customer route assignments | Yes | Yes | No | No | No | No |
| `routing.assignment.edit` | Edit, delete, or rollback customer route assignments | Yes | Yes | No | No | No | No |
| `routing.assignment.approve` | Approve/reject customer route assignment changes | Yes | No | Yes | No | No | No |
| `routing.rule.view` | View routing rules, history, and exports | Yes | Yes | Yes | No | No | No |
| `routing.rule.create` | Create conditional routing rules | Yes | Yes | No | No | No | No |
| `routing.rule.edit` | Edit, delete, or rollback conditional routing rules | Yes | Yes | No | No | No | No |
| `routing.rule.activate` | Activate/deactivate routing rules | Yes | Yes | Yes | No | No | No |
| `routing.rule.approve` | Approve/reject routing rule changes | Yes | No | Yes | No | No | No |
| `routing.simulate` | Execute route decision simulator (audited) | Yes | Yes | Yes | No | No | No |
| `routing.killswitch` | Disable connection immediately; request re-enable | Yes | No | Yes | No | No | No |
| `routing.snapshot.view` | View snapshot status and coverage gap report | Yes | Yes | Yes | No | No | No |
| **`routing.override`** | **Global routing Maker-Checker bypass** (see §2.1) | **Yes** | **No** | **No** | **No** | **No** | **No** |
| **`routing.entry.override`** | **Entry-only Maker-Checker bypass** (see §2.1) | **Yes** | **No** | **No** | **No** | **No** | **No** |

### 2.1 Override Permissions: `routing.override` vs. `routing.entry.override`

1. **`routing.override`**:
   - **Held by**: `Super Admin` (`Platform Admin`) only. Maker roles (`Routing Maker`, `Master Data Editor`) and Checker roles (`Routing Checker`, `Master Data Approver`) **never** hold this permission.
   - **What it bypasses**: Bypasses the Maker-Checker approval queue across all routing entities:
     - Route plan activation (`RequestPlanActivation`) and deactivation (`RequestPlanDeactivation`)
     - Route plan entry creation (`CreateEntry`), modification (`UpdateEntry`), and deletion (`DeleteEntry`) on an **active** route plan
     - Customer route assignment creation (`CreateAssignment`), modification (`UpdateAssignment`), and deletion (`DeleteAssignment`)
     - Routing rule creation (`CreateRule`), modification (`UpdateRule`), and deletion (`DeleteRule`)
   - **What it does NOT bypass**: Emergency kill-switch re-enablement (`EnableConnection`) always creates a `connection_reenable` change request requiring independent Checker approval.

2. **`routing.entry.override`**:
   - **Held by**: `Super Admin` (`Platform Admin`) only (or a custom senior routing role explicitly granted by a Super Admin). Maker roles (`Routing Maker`, `Master Data Editor`) and Checker roles (`Routing Checker`, `Master Data Approver`) **never** hold this permission.
   - **What it bypasses**: Bypasses the Maker-Checker approval queue **only** when adding (`CreateEntry`), updating (`UpdateEntry`), or removing (`DeleteEntry`) an entry on an **active** route plan.
   - **What it does NOT bypass**: Does **not** bypass approval for route plan activation/deactivation, customer route assignments, routing rules, or kill-switch re-enablement.


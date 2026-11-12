# Phase 9 "Visibility" Progress Tracker (`docs/PHASE9_PROGRESS.md`)

| Stage | Title | Target Duration | Status | Started At | Completed At | Notes |
|---|---|---|---|---|---|---|
| Stage 0 | Preflight, Baseline, Triage | ~45 min | Completed | 2026-11-12 17:08 | 2026-11-12 18:20 | IDE clean, metrics, alerts.yml, security-scan, dev-upgrade, baseline PASS |
| Stage 1 | Design Document First (`docs/DASHBOARD_DESIGN.md`) | ~45 min | Completed | 2026-11-12 18:20 | 2026-11-12 18:25 | Source of truth, event-time, 15m rollups, partitions, performance budgets |
| Stage 2 | Data Foundation (Migrations, Rollup Engine) | ~90 min | In Progress | 2026-11-12 18:28 | | Migration 15+, differential test |
| Stage 3 | Analytics APIs (Dashboards, Reports, Registry) | ~120 min | Pending | | | Tenant isolation, leak scan, p95 budgets |
| Stage 4 | Live Hub (WebSocket, Real-time TPS) | ~60 min | Pending | | | Auth, fan-out, client isolation |
| Stage 5 | Notification Center (In-app, SMTP sink, Webhook) | ~90 min | Pending | | | Preferences, categories, storm protection |
| Stage 6 | Alert Engine, Incidents, Insights (`docs/ALERTING.md`) | ~150 min | Pending | | | Rules, flapping, escalations, timeline |
| Stage 7 | Settings Center (Typed Registry, Admin UI) | ~75 min | Pending | | | Company, product, provider, billing, authz |
| Stage 8 | Search, Saved Filters, Preferences, i18n | ~90 min | Pending | | | Global search, filter compiler, Bangla (bn) |
| Stage 9 | Export Center & Scheduled Reports | ~60 min | Pending | | | CSV/XLSX/JSON, formula safety, scheduling |
| Stage 10 | Frontend (Accessible SVG Charts, Dashboards, Shell) | ~180 min | Pending | | | Responsive, light/dark, svelte-check 0 |
| Stage 11 | Demo Stack & Walkthrough (`docs/PHASE9_WALKTHROUGH.md`) | ~60 min | Pending | | | znero_demo, demo traffic, operator guide |
| Stage 12 | Cross-Cutting Verification (Authz, Fuzz, Chaos) | ~90 min | Pending | | | 385+ routes matrix, 100k burst guard |
| Stage 13 | Browser Verification (Playwright, Themes, Viewports) | ~150 min | Pending | | | 1440/820/390px, UI audit, full flows |
| Stage 14 | Scale and Soak (>=90m soak, 1M msgs) | ~120 min | Pending | | | soak telemetry, memory flat, p95 budgets |
| Stage 15 | Docs, Compliance, Upgrade | ~60 min | Pending | | | Spec compliance, upgrade dev database |
| Backlog | Continuous hardening until 10-15h mark | As needed | Pending | | | Differential fuzz, timezones, bundle split |

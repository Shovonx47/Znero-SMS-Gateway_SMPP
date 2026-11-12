# Znero International — Security Review (`docs/SECURITY_REVIEW.md`)

**Date**: 2026-11-12
**Scope**: Full backend (`Go`), frontend (`SvelteKit`), PostgreSQL (`znero_test`), RBAC matrix (`385` HTTP routes), Customer Edge API & SMPP gateway, SSRF egress controls, cryptography/secrets handling, and dependency audit.

---

## 1. Threat Model & Key Trust Boundaries

1. **Public Internet -> Customer Edge HTTP/SMPP (`8080 / 2776`)**: Untrusted external tenant traffic authenticated via Bearer API keys (`znk_live_...`, SHA-256 hashed at rest, constant-time compare, per-key `ip:prefix` lockout) or SMPP `system_id` / password (Argon2id hashed at rest, IP allowlist, bind caps).
2. **Browser Portal -> Dashboard API (`/api/v1/...`)**: Multi-tenant hierarchy (`platform` -> `reseller` -> `customer`) authenticated via short-lived JWT access tokens + rotating `HttpOnly`, `SameSite=Strict`, `Secure` (non-dev) refresh cookies with RFC 6819 family reuse revocation and `Origin`/`Referer` CSRF validation.
3. **Egress Connections (`smpp.SafeDialer`, `httpprovider.Client`, `webhook.Dispatcher`)**: User-configured upstream carrier URLs/hosts and customer webhook URLs guarded against SSRF at both configuration/registration time and TCP `DialContext` + socket `Control()` time.
4. **Data Store (`PostgreSQL`)**: Non-superuser `znero_app` role, prepared SQL statements (`pgx`), append-only audit logs (`UPDATE`/`DELETE`/`TRUNCATE` revoked), and AES-256-GCM encryption at rest (`secrets.KeyRing`).

---

## 2. Security Controls Verified in Part 5

All automated assertions below ran and passed (`-race -p=1`) against live PostgreSQL `znero_test` in [security_hardening_test.go](file:///e:/Softcents%20Systems/Znero%20International/backend/internal/app/security_hardening_test.go), [authz_matrix_test.go](file:///e:/Softcents%20Systems/Znero%20International/backend/internal/app/authz_matrix_test.go), [integration_test.go](file:///e:/Softcents%20Systems/Znero%20International/backend/internal/auth/integration_test.go), and module suites:

| Security Domain | Verification Scope | Observed Outcome |
| :--- | :--- | :--- |
| **RBAC Route Matrix** | Dynamically walked `385` unique Fiber HTTP routes across 5 principals (`unauth`, `platform_admin`, `platform_maker`, `reseller_admin`, `customer_admin`) | **PASS** — unauth `401`, maker self-approve `403`, reseller/customer blocked from platform/billing-terms endpoints `403`. |
| **Cross-Tenant IDOR Matrix** | Reseller B & Customer B1 vs Reseller A & Customer A1 IDs across `organizations`, `users`, `roles`, `customer-profiles`, `customer-api-keys`, `customer-smpp-accounts`, `sender-ids`, `wallets`, `invoices`, `providers`, `routing/plans`, and Customer Edge `/customer/webhooks/:id` (`GET`/`PUT`/`DELETE`/`test`/`deliveries`) | **PASS** — all rejected with `403` or `404`; 0 tenant-A data leaked; victim webhook confirmed intact after cross-tenant `DELETE`. |
| **Customer Edge Isolation** | Confirmed JWT rejected on `/api/v1/customer/...` (`401`), scopes enforced (`sms:send` vs `account:read`), and `/customer/account` exposes read-only `billing_terms` with zero `provider` / `buy_price` / `margin` fields | **PASS** — zero commercial or provider leakage. |
| **Injection, Mass Assignment & DoS** | SQLi payloads on queries/UUID paths, privilege escalation (`POST /roles` with platform-only permissions), invalid webhook status (`400`), real TCP `BodyLimitBytes` overflow (`413`), CRLF `X-Request-ID` replacement | **PASS** — `0` HTTP 500s, DB intact, oversized body rejected with `413`, sanitized `X-Request-ID`. |
| **CORS, CSRF, Cookies & Refresh Reuse** | `EnvProduction` app test: negative `Origin` (`https://evil.example`) preflight rejected, allowed `Origin` reflected, security headers (`nosniff`, `DENY`, `no-referrer`, strict `CSP`), CSRF blocked on `/refresh` & `/logout` (`403`), `Set-Cookie` flags (`HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/api/v1/auth`), stale refresh replay revoked entire session family | **PASS** (`0.50s`). |
| **SSRF & DNS Rebinding** | Tested `26` SSRF URLs on production-mode `POST /api/v1/customer/webhooks`, `provider.ValidateHostSSRFWithOptions`, and `smpp.NewSafeDialer` (`169.254.169.254`, `10/8`, `172.16/12`, `192.168/16`, `0.0.0.0`, `::1`, `fc00::/7`, `fe80::/10`, `[::ffff:127.0.0.1]`, `[::ffff:169.254.169.254]`, `0177.0.0.1`, `0x7f.0.0.1`, `0x7f000001`, `2130706433`, `127.1`, `127.0.0.1.`, `localhost.`, `user:pass@`, `http://`, `file://`, `gopher://`, CRLF, mixed-IP DNS rebinding) | **PASS** — all rejected (`400` / error). |
| **Webhook HMAC & Log Redaction** | Verified `X-Znero-Timestamp` + `.` + body HMAC-SHA256 `v1=` signature matches `hmac.Equal` and `testkit.LogCapture` contains zero API keys, webhook secrets, JWT secrets, passwords, or PII | **PASS**. |

---

## 3. Security Bugs Discovered & Fixed

| Bug ID | Severity | Finding & Fix Summary |
| :--- | :--- | :--- |
| **`BUG-046`** | **HIGH** | `system.info.view` was omitted from `rbac.platformOnlyExact` (causing 38 legacy role grants in `znero_test`), and `GET /api/v1/system/info` lacked `rbac.RequirePermission("system.info.view")`. Fixed in `rbac/platform_only.go` + `app/app.go` + `scripts/repair-legacy-rbac-permissions.sql` (purged all 38 non-platform grants in `znero_test` -> `0` remaining). |
| **`BUG-047`** | **HIGH** | `POST`/`PUT /api/v1/customer/webhooks` in `customeredge/handler.go` accepted arbitrary `http://`/`https://` prefixes without SSRF/userinfo/CRLF/status checks. Enforced `provider.ValidateHTTPBaseURLWithOptions` + `EdgeHandler.SetAllowLoopback` (fail-closed in production) + status allowlist (`active`/`disabled`), plus `provider/ssrf_safety.go` for obfuscated numeric IPv4/trailing-dot vectors. |
| **`BUG-048`** | **LOW** | Cross-tenant `DELETE /api/v1/customer/webhooks/:id` returned `204` and `GET .../deliveries` returned `200` (`items: null`) instead of `404`. Added `GetWebhook` ownership check returning `404` and non-nil slice initialization. |
| *Auth REST mapping* | **LOW** | Mapped out-of-scope `"user not found"` on `SuspendUser`/`ActivateUser`/`UpdateUserRoles` from `400` to `404` (`auth/handler.go`). |

---

## 4. Dependency & Static Tooling Status

- **Backend Go static analysis**: `go vet ./...` and `golangci-lint run ./...` pass with **0 issues**.
- **`govulncheck` / `gosec`**: **NOT INSTALLED** in the local Go toolchain (`Get-Command` verified absent; no external binary installs performed during unattended run). Recommended to run `govulncheck ./...` in CI where `proxy.golang.org` access is configured.
- **Frontend `npm --prefix frontend audit`**: reported `9` vulnerabilities (`6 low` via `@sveltejs/kit`/`cookie`, `1 moderate` & `2 critical` strictly inside test devDependencies `vitest`/`tinypool`, remediation requires breaking major upgrades `@sveltejs/kit@3.2.0` / `vitest@5.1.0`; `0` runtime production `high`/`critical` vulnerabilities).
- **Local `znero_dev` advisory**: `scripts/repair-legacy-rbac-permissions.sql` was verified and run against `znero_test` only (`znero_dev` was untouched per run rules; operators should run `repair-legacy-rbac-permissions.sql` once against `znero_dev` or staging DBs before reuse).

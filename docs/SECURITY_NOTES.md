# Znero International — Security Notes & Guidelines

---

## 1. Database Security & Findings

### 1.1 PostgreSQL Authentication (`pg_hba.conf`) Inspection Findings
- **Local Machine Inspection Results:**  
  A detailed audit of `C:\Program Files\PostgreSQL\17\data\pg_hba.conf` revealed the following host lines:
  ```text
  TYPE  DATABASE        USER            ADDRESS                 METHOD
  local all             all                                     trust
  host  all             all             127.0.0.1/32            trust
  host  all             all             ::1/128                 trust
  ```
- **Analysis:**  
  Only loopback connections (`127.0.0.1/32` and `::1/128`) are trusted. There are no rules allowing external IPv4/IPv6 subnets, so inbound network connections from foreign IPs are rejected by PostgreSQL with `no pg_hba.conf entry for host`.
- **`listen_addresses` Finding:**  
  `postgresql.conf` has `listen_addresses = '*'`. While `pg_hba.conf` rejects untrusted remote addresses, port `5432` binds to `0.0.0.0`.
- **Recommendations:**  
  1. For single-user local development, leave `pg_hba.conf` as-is to avoid accidental lockout or administrative credential elevation.
  2. For defense-in-depth on dev machines, set `listen_addresses = 'localhost'` in `postgresql.conf` so port 5432 is not exposed on local network adapters.
  3. **Production Requirement (CRITICAL):** Production deployments **MUST NEVER** use `trust` authentication. In all staging and production environments, PostgreSQL must strictly enforce `scram-sha-256` authentication for all roles. Plaintext or md5 password hashing is strictly prohibited.

### 1.2 Principle of Least Privilege
- The application connects exclusively using a dedicated non-superuser role: `znero_app` (`rolsuper = f`, `nocreatedb`, `nocreaterole`).
- The application code never uses the `postgres` superuser.
- Table permissions on `audit_logs` are strictly restricted: `UPDATE`, `DELETE`, and `TRUNCATE` are revoked from `znero_app`.

---

## 2. Secrets & Configuration Hygiene

- **No Secrets in Source Control:**  
  All sensitive configuration (passwords, connection strings, keys, secrets) is loaded dynamically via environment variables (`.env`).
  - `.env` is explicitly ignored in [`.gitignore`](file:///e:/Softcents%20Systems/Znero%20International/.gitignore).
  - [`.env.example`](file:///e:/Softcents%20Systems/Znero%20International/.env.example) contains only dummy placeholders and is committed to document required configuration keys.
- **Log Masking:**  
  The configuration loader implements `slog.LogValuer` and `RedactedDatabaseURL()`. Any attempt to log the configuration object automatically redacts database passwords.
- **Error Obfuscation:**  
  Internal error messages (e.g. database connection strings or SQL syntax errors) are logged strictly server-side and never returned to the client in HTTP responses. Clients receive generic, stable error codes (e.g. `internal_error`, `service_unavailable`).

---

## 3. Web & Network Security

### 3.1 Default-Deny Method Architecture
- **HTTP Method Allow-List:**  
  The HTTP server enforces an explicit allow-list: `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`. Unsupported or dangerous HTTP methods (`TRACE`, `CONNECT`) receive `HTTP 405 Method Not Allowed` with an `Allow` header.
- **API Versioning:**  
  All business endpoints live strictly under `/api/v1`. Unversioned root routes are reserved exclusively for infrastructure probes (`/healthz`, `/readyz`).
- **Security Headers:**  
  Every response from the Fiber backend includes:
  - `X-Content-Type-Options: nosniff`
  - `X-Frame-Options: DENY`
  - `Referrer-Policy: no-referrer`
  - `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`
  - `Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=()`
  - `Cache-Control: no-store`

### 3.2 Request Tracking & Log Injection Defense
- Every inbound request is assigned a unique, cryptographically random 32-character hexadecimal ID if not provided, or validated against an allow-list regex (`^[A-Za-z0-9._-]{8,64}$`).
- The `X-Request-ID` is returned on the response and propagated via `context.Context` to every `slog` record emitted during the lifecycle of that request.
- Payloads from external systems (such as Kafka messages) are logged as structured JSON attributes with bounded length (truncated at 512 bytes), preventing newline carriage return injection.

### 3.3 CSRF & CORS Defenses
- Refresh tokens are transmitted in an `httpOnly`, `SameSite=Strict` cookie (`Secure` flag on outside development).
- State-changing cookie-authenticated endpoints verify `Origin` against `CORS_ALLOWED_ORIGINS` to protect against cross-site request forgery.
- CORS is strictly limited to exact allowed origins (wildcards are forbidden by the config validator).

---

## 4. Multi-Tenant Boundary Scoping

- **Server-Derived Context:** The client is never allowed to specify `tenant_id` to determine data access rights. Identity and tenant boundaries are derived strictly from verified cryptographic tokens on the server.
- **Materialized Path Scoping:** All tenant queries enforce materialized path prefix matching (`/{root_id}/{child_id}/`). An organization can see only itself and its descendants; customer leaf organizations cannot see siblings or parents.
- **Row-Level Security (RLS) vs Application Scoping:** Multi-tenancy scoping is enforced systematically in the Go data-access layer via `tenant.Context` and `OrgPathFilter()`. This avoids connection pool contamination risks associated with session-level PostgreSQL RLS variables while guaranteeing compile-time type safety.
- **Tree Revocation:** Suspending an organization cascades downward, setting all descendant organizations to `suspended` and immediately revoking all active sessions in the entire organization subtree.

---

## 5. Authentication, Passwords & Sessions

- **Argon2id Hashing:** Password hashing uses RFC 9106 recommended parameters (memory: 64 MB, iterations: 3, parallelism: 2, salt: 16 bytes random). Passwords must be at least 12 characters and are checked against a common-password blacklist.
- **Timing Attack Mitigation:** Authentication evaluates constant-time dummy Argon2id comparisons when an account does not exist, ensuring response timings do not leak user enumeration.
- **Account Lockout:** 5 failed login attempts trigger an automatic 15-minute temporary lockout.
- **In-Memory Access Tokens:** JWT access tokens (~15m expiry) are stored exclusively in client memory, never persisted in `localStorage` or `sessionStorage` (mitigating XSS extraction).
- **Session Rotation & Reuse Detection (RFC 6819):** Refresh tokens rotate on every use. Presenting an already-rotated or revoked token hash immediately triggers family revocation, terminating all sessions in that family.
- **Instant Revocation:** Suspending a user or calling `/auth/logout-all` takes effect immediately. The authentication middleware verifies active status against the database on every request.

---

## 6. Two-Factor Authentication (RFC 6238 TOTP)

- **Mandatory for Super Admins:** Platform Super Admins are strictly required to complete MFA verification.
- **AES-256-GCM Secret Encryption at Rest:** TOTP enrollment secrets are encrypted at rest using AES-256-GCM with a 32-byte (64 hex character) key configured in `MFA_ENCRYPTION_KEY`.
- **Hashed Recovery Codes:** 10 single-use recovery codes are generated per enrollment and stored only as SHA-256 hashes in `user_recovery_codes`. Once used, a recovery code is permanently consumed.
- **Two-Step Login:** Short-lived (5-minute) challenge tokens decouple initial credential verification from TOTP evaluation.

---

## 7. Role-Based Access Control (Default Deny)

- **Default-Deny Policy:** Permissions follow `module.resource.action`. Routes without an explicit permission policy are denied by default.
- **System Role Protection:** System roles (`Super Admin`, `Org Admin`, `Staff`, `Read-only`) cannot be modified or deleted.
- **Privilege Escalation Prevention:** An actor cannot create, update, or assign roles with permissions the actor does not personally possess.

---

## 9. Housekeeping & Credential Leak Safeguards (Phase 3 Audit)

- **Leak Remediation:**  
  A repository-wide security scan audited all files (migrations, docs, reports, tests, frontend, scripts) for hardcoded credentials, passwords, TOTP keys, and recovery codes.
  - Previous Phase 2 test password (`Barcelona1234#`) was completely redacted from markdown reports and artifacts.
  - Hardcoded platform admin emails (`shovonislam493@gmail.com`) in login placeholders were replaced with generic examples (`admin@example.com`).
  - Production bootstrap admin account is untouched and strictly isolated.
- **Isolated Dev Seed Account Provisioner:**  
  All fake development test accounts are moved into `backend/cmd/seeddev/main.go` and `scripts/seed-dev-data.ps1`.
  - The tool explicitly refuses to run unless `APP_ENV=development`.
  - Accounts are strictly marked as fake (`@dev.znero.test`).
  - Contains an idempotent `-Cleanup` switch to purge all fake accounts.
- **Startup Environment Safety Check:**  
  The backend API entrypoint (`backend/cmd/api/main.go`) contains a startup safety check: if `APP_ENV != development` and any `@dev.znero.test` or dev fake accounts are detected in PostgreSQL, it logs a prominent high-priority security warning to notify administrators immediately.

---

## 10. Master Data, Import & Number Engine Security

- **Dual-Control Maker-Checker Enforcement:**  
  Master data modifications (countries, operators, prefixes) submitted by users without the override permission are routed into pending `change_requests`.
  - The maker can never approve their own change request (`ErrMakerCannotApprove`), strictly enforced by the backend service and database constraint.
  - Rejection requires an explicit mandatory reason.
- **Spreadsheet Formula Injection Defense:**  
  All user-supplied text exported to CSV or Excel is sanitized against CSV Injection (CWE-1236). Any cell value beginning with `=`, `+`, `-`, `@`, `\t`, or `\r` is automatically prepended with a single quote (`'`) to disable formula execution in spreadsheet software.
- **Import Security & File Validation:**  
  - **Size Limit:** Max upload size is strictly capped at 50MB (`MaxFileSize = 50 * 1024 * 1024`).
  - **Path Traversal Defense:** `filepath.Base()` strips directory elements from uploaded file names, preventing path traversal attacks.
  - **Memory Safeguards:** Uploaded Excel and JSON streams use `io.LimitReader` to prevent decompression bombs or memory exhaustion attacks.
  - **Dry-Run Preview Pipeline:** Nothing is written to the live database during initial upload. The file is validated, checked for internal duplicates and DB conflicts, and presented as a preview report before explicit user confirmation.
  - **Platform-Only Scope:** Reseller and Customer organizations have read-only access to active master data. Direct import and master data mutations return `403 Forbidden`.
- **In-Memory Trie Concurrency Safety:**  
  The number intelligence longest-prefix trie utilizes Go's `sync/atomic.Pointer` (`atomic.Pointer[trieNode]`). Reads during high-throughput SMS routing lookups are lock-free and zero-allocation. Reloading the trie builds a new immutable tree in memory and performs a single atomic pointer swap, guaranteeing zero lock contention and no partial read corruption.

---

## 11. Phase 4 Security: Envelope Encryption, One-Time Credentials & SSRF Defense

- **AES-256-GCM Keyring & Envelope Encryption (`backend/internal/secrets`):**
  - Provider connection credentials (`smpp_password_encrypted`, `http_credentials_encrypted`) are encrypted at rest using AES-256-GCM with a random 96-bit nonce per encryption operation and versioned key identifiers (`v1:<base64(nonce || ciphertext || tag)>`).
  - Keys are loaded exclusively from environment variables (`SECRETS_ENCRYPTION_KEY`, `SECRETS_KEY_ID`, `SECRETS_PREVIOUS_KEYS`). `RotateEnvelope` re-encrypts envelopes from retired keys to the active key ID without exposing plaintext to callers.
  - **Write-Only Provider Secrets:** API `GET` responses never return decrypted provider passwords or ciphertexts; they return only `SecretStatus` metadata (`{ is_set: true, changed_at, masked_display: "••• set" }`).
- **One-Time Customer Credentials (`customer_api_keys`, `customer_smpp_accounts`):**
  - Customer HTTP API keys (`znk_live_<base64url>`) and SMPP bind passwords are generated cryptographically via `crypto/rand` and returned in plaintext **only once** in the HTTP creation/rotation response.
  - Only the 12-character key prefix and SHA-256 hash (for API keys) or Argon2id hash (for SMPP passwords) are persisted in PostgreSQL. Neither the user nor a platform administrator can retrieve the plaintext secret after the creation modal is closed.
- **Secret-Free Audit & Version Snapshots:**
  - `provider_connections_history`, `change_requests`, and `audit_logs` never store plaintext or ciphertext credentials. Credential mutations record only `"credential_changed": true` and the timestamp.
  - Importing credentials via CSV/XLSX/JSON is permanently forbidden (`ErrCredentialImportDenied`).
- **SSRF & Network Parameter Validation:**
  - All provider SMPP hosts, provider HTTP base URLs, and customer webhook callback URLs are validated by `ValidateHostSSRF` and `ValidateHTTPBaseURL`.
  - Loopback (`localhost`, `127.0.0.0/8`, `::1`), RFC 1918 private networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local / cloud metadata endpoints (`169.254.0.0/16`, `fe80::/10`), CGNAT (`100.64.0.0/10`), and non-HTTPS callback schemes are rejected with `400 Bad Request`.
  - `ValidateHostSSRF` performs DNS resolution with a 2-second timeout; if any resolved IP falls into a private, loopback, or cloud-metadata CIDR range, an SSRF validation error is returned.
  - **Phase 8 Connection-Time SSRF Enforcement (CRITICAL):** Because DNS rebinding can change hostname resolution between configuration time and network transport, the destination IP address MUST be re-checked against SSRF CIDRs at connection/dial time in Phase 8 before establishing outbound SMPP or HTTP sockets.
- **Platform-Only Provider Isolation & Hierarchical Customer Scoping:**
  - Upstream providers, provider connections, and provider coverage are strictly restricted to the `platform` organization (`!tc.IsPlatformOrg()` returns `403 Forbidden` even if a non-platform role were misconfigured).
  - Customer commercial profiles, API keys, SMPP accounts, and Sender IDs are scoped by materialized organization path (`platform` sees all; `reseller` sees only its own customer subtree; `customer` sees only its own organization).
  - **Platform-Only Customer Profile Fields & Reseller Ceilings:**
    - `account_type`: Only platform administrators may set or alter account type to "postpaid" (`ErrPlatformOnlyField`); resellers may only assign "prepaid".
    - `currency_code`: Platform-only commercial parameter.
    - `billing_terms_days`: Platform-only commercial parameter.
    - `status`: Reactivating profiles from "suspended" or "closed" status is restricted to platform administrators.
    - Resellers are bound by strict resource ceilings:
      - Max TPS: capped at 200 (`MaxResellerTPS`)
      - Credit Limit: capped at 50,000.00 (`MaxResellerCreditLimit`)
      - Daily Message Cap: capped at 1,000,000 (`MaxResellerDailyCap`)
      - Monthly Message Cap: capped at 20,000,000 (`MaxResellerMonthlyCap`)
    - Customer users hold read-only access to their own customer profile and cannot mutate commercial limits or profile metadata.

---

## 12. Phase 6 Security: Double-Entry Ledger, Financial Immutability & Billing Isolation

- **Trigger-Enforced General Ledger Immutability:**
  - `ledger_accounts`, `ledger_transactions`, and `ledger_entries` are permanent financial books of record. PostgreSQL triggers (`trg_ledger_accounts_immutable`, `trg_ledger_transactions_immutable`, `trg_ledger_entries_immutable`) strictly block `UPDATE` and `DELETE` queries, raising database exceptions.
  - Corrections are recorded exclusively as append-only `reversal` transactions pointing to `original_transaction_id`. Attempting to reverse an already reversed transaction is refused with `ErrAlreadyReversed`.
- **Issued Invoice Immutability:**
  - Database triggers `trg_invoices_immutable` and `trg_invoice_lines_immutable` block modifications and deletions to invoices and line items once an invoice is issued or paid. Corrections require formal credit notes (`CN-YYYY-XXXXXX`). Voiding an invoice requires an explicit reason and Maker-Checker approval.
- **Concurrency & Overdraft Prevention:**
  - Balance updates and hold reservations acquire exclusive PostgreSQL row locks (`SELECT ... FOR UPDATE`) on the target wallet within an atomic transaction.
  - Prepaid accounts strictly reject transactions where balance would drop below zero (`ErrPrepaidOverdraft`). Postpaid accounts enforce credit limits read from the customer profile as the single source of truth (`ErrPostpaidCreditLimitExceeded`).
  - Available balance incorporates active holds (`Available = Balance - ActiveHolds + CreditLimit`).
- **Idempotency Guarantees:**
  - Unique constraints on `(tenant_org_id, transaction_type, idempotency_key)` prevent duplicate financial operations. Replays with identical parameters return the cached result cleanly; replays with mismatched parameters return `409 Conflict`.
- **Maker-Checker Dual Control:**
  - Top-ups and adjustments without `wallet.override` generate `approval_requests`. The initiator is procedurally and cryptographically blocked from approving their own request (`ErrMakerCannotApprove`).
- **Confidentiality & Anti-Leakage:**
  - Provider buy rates, upstream supplier IDs, and margins are stripped at the data layer and never rendered in customer or reseller statements or invoice line items.
  - Spreadsheet formula injection protection (`'`, `+`, `-`, `=`, `@` prepended with a single quote) is applied to all CSV and JSON statement exports.

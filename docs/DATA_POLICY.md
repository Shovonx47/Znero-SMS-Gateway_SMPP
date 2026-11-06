# Znero International — Data Policy & Reference Integrity

## 1. Absolute Prohibition on Fabricating Operator Data
**Platform Rule: Under no circumstances may MCC/MNC codes, operator names, or routing prefixes be invented or hallucinated.**
* Real-world telecommunication switching and SMS routing rely on precise regulatory and contractual number assignments.
* Fictional, guessed, or hallucinated operator or prefix data can lead to catastrophic misrouting, regulatory penalties, or security violations.
* Real operator data in production will **only** be imported from licensed, verified telecom registry data feeds (e.g., ITU Operational Bulletin, GSMA Infocentre, or licensed carrier routing databases).

## 2. Authoritative Country Reference Source
* Country baseline records are sourced strictly from authoritative, publicly verifiable standards:
  - **ISO 3166-1 alpha-2, alpha-3, and numeric-3 codes**: International Organization for Standardization (ISO).
  - **ITU-T Recommendation E.164**: International Telecommunication Union (ITU) international public telecommunication numbering plan country calling codes.
* Reference Data Source Tag: `ISO-3166-1-ITU-E164`.
* Initial Verification State:
  - All automated or bulk-imported country records start with `verification_status = 'unverified'`.
  - A human operator or designated platform compliance admin must inspect and verify the regulatory and numbering policies before marking the record as `verified`.

## 3. Separation of Concerns: Master Data vs. Coverage & Connectivity
* Country existence and operator identification are **pure reference entities**.
* **Do NOT conflate reference data with commercial availability or live connectivity**:
  - A country record represents the geographical entity and numbering plan, NOT whether Znero has commercial SMS coverage or active routes there.
  - An operator record represents a physical or virtual carrier with an MCC/MNC pair, NOT a supplier connection.
  - **Provider Coverage (`provider_coverage`)** is stored separately from master data and represents an explicit provider-to-country (and optional operator) link with a lifecycle status (`declared` → `tested` → `active` / `suspended`).
  - **Phase 4 Configuration vs. Phase 8 Live Connectivity**: Phase 4 stores validated provider connection configurations (`provider_connections`), customer commercial profiles (`customer_profiles`), customer credentials (`customer_api_keys`, `customer_smpp_accounts`), and Sender ID registrations (`sender_ids`). Nothing in Phase 4 opens a live network socket or binds an SMPP session; live SMPP/HTTP traffic execution belongs to Phase 8.

## 4. Zero-Fabrication Policy for Providers, Connections, Credentials & Sender IDs
* **Never invent real-world provider names, SMPP/HTTP endpoints, credentials, system IDs, IP addresses, or customer Sender IDs.**
* Sample Phase 4 records (`DEV SAMPLE Provider Alpha`, `DEV SAMPLE Provider Beta`, coverage rows, sample customer commercial profile, and `DEV-SMS` Sender ID):
  - Are explicitly prefixed/tagged with `DEV SAMPLE` (`data_source = 'DEV SAMPLE'`).
  - Use strictly reserved documentation/testing domains (`smpp-alpha.example.com`, `https://api-beta.example.com/v1/sms`).
  - Generate random secrets at runtime via `crypto/rand` before encrypting them through the service layer — no static sample passwords exist in code or seed files.
  - Are loaded **only** when `APP_ENV=development` via `scripts/seed-providers-sample.ps1` (or `go run ./cmd/seedproviderssample`), with a matching `-Cleanup` command.
* **Credentials & Secrets Policy**:
  - **Provider connection secrets** (SMPP passwords, HTTP auth credentials) are encrypted at rest with AES-256-GCM (`SECRETS_ENCRYPTION_KEY`) and are strictly **write-only**: API `GET` responses return only `{ is_set, changed_at, masked_display: "••• set" }`.
  - **Customer credentials** (HTTP API keys and SMPP account passwords) are **shown once at creation/rotation** and stored only as cryptographic hashes (SHA-256 for API keys with visible prefix; Argon2id for SMPP passwords).
  - **Import Prohibition**: Importing credentials via CSV/XLSX/JSON is permanently blocked (`ErrCredentialImportDenied`).
  - **Zero Plaintext in History/Audit**: Version snapshots (`*_history`), `change_requests`, and `audit_logs` never contain plaintext or ciphertext secrets; they record only `"credential_changed": true` and a timestamp.

## 5. Development & Local Testing Samples
* Operators, prefixes, and provider samples shipped with the codebase are strictly restricted to a tiny set of dev sample rows:
  - Labeled with `DEV-SAMPLE-FICTIONAL` / `DEV SAMPLE`.
  - Stored under `data/samples/` or generated via `cmd/seedmastersample` and `cmd/seedproviderssample`.
  - Seeded **only** when `APP_ENV=development` via explicit commands (`scripts/seed-master-sample.ps1`, `scripts/seed-providers-sample.ps1`).
  - Both seed scripts refuse execution if `APP_ENV != development` and provide `-Cleanup` flags.
* A startup security check in `backend/cmd/api/main.go` logs a loud warning if sample or dev seed accounts are detected in non-development environments.

## 6. Auditability and Immutable Version History
* Every creation, edit, status transition, and rollback of countries, operators, prefixes, providers, provider connections, provider coverage, customer profiles, and sender IDs creates an immutable snapshot in the respective `_history` table (`countries_history`, `operators_history`, `number_prefixes_history`, `providers_history`, `provider_connections_history`, `provider_coverage_history`, `customer_profiles_history`, `sender_ids_history`).
* Rollbacks create a new version ($N+1$) rather than deleting or rewriting past versions.
* Audit entries are recorded in the central audit system within the exact same database transaction.

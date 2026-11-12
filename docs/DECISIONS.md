# Architectural & Implementation Decisions (`docs/DECISIONS.md`)

Recorded safe defaults and engineering decisions made during the Znero International verification and feature build.

---

## DEC-001: Existing Customer Backfill for Per-Customer Billing Terms
- **Decision**: Migration `000013_per_customer_billing_terms` backfills all existing `customer_profiles` rows using the current global `system_settings` keys (`billing.sms.customer_trigger`, fallback `'on_delivery'`) and `NO_DLR_HOLD_POLICY` (`'retain_until_resolution'`), `dlr_wait_hours = 24`, and `failed_after_accept_policy = 'keep_charge'`. New customer profiles inherit platform defaults configured in `customer.Service` and loaded at boot in `cmd/api/main.go`.
- **Rationale**: Preserves 100% behavioral backwards compatibility for existing customers and tests while transferring future configuration authority to per-customer platform-only settings.

## DEC-002: `retain_until_resolution` Transitions to `unknown` (Not `expired`) After `dlr_wait_hours`
- **Decision**: For `on_delivery` messages whose `dlr_wait_hours_snapshot` elapses with no receipt and `no_dlr_policy_snapshot == 'retain_until_resolution'`, the reaper transitions `submitted -> unknown` (adding `submitted -> unknown` to the state machine), retains the active hold, and records a `no_dlr_retained_exposure` settlement exception. Once `UnknownResolutionWindow` also expires, `reapUnknownMessages` releases the hold and finalizes exposure review. Late DLRs arriving while `status = 'unknown'`? Because `unknown` is protected unless reconciled (or if strictly final, late DLR logs `dlr_conflict` / audit review), Wait: in `reaper.go` / `dlr.go` we allowed `StatusUnknown` retention before `UnknownResolutionWindow`, keeping holds intact until final disposition.
- **Rationale**: Matches wholesale SMS liability semantics: no handset delivery confirmation within SLA means unknown outcome while carrier DLRs might still arrive shortly after TTL, whereas `immediate_release` and `capture_at_ttl` settle definitively as `expired`.

## DEC-003: `failed_after_accept_policy = 'refund'` Accounting Entry
- **Decision**: Implemented `ledger.Service.RefundCaptureTx` which checks the originating hold is `'captured'`, creates a balanced double-entry transaction (`transaction_type = 'refund'`) debiting `PLATFORM_REVENUE[_EUR]` (derived from the hold currency) and crediting the customer wallet, and sets `holds.status = 'refunded'` (added to `holds_status_check` in Migration `000013`).
- **Rationale**: Keeps the ledger strictly balanced (zero-sum double-entry invariant), preserves full auditability of both original capture and subsequent refund, and enables deterministic reconciliation (`reconcile.go` permits `refunded` holds cleanly).

## DEC-004: HTTP Provider Auth Types Backwards-Compatible Naming
- **Decision**: Migration `000014` expands `provider_connections.http_auth_type` CHECK constraint to support `none`, `basic`, `bearer`, `api_key_header`, `api_key_query` alongside legacy aliases `header_token` and `query_token`.
- **Rationale**: Satisfies exact Step 3 requirement wording (`none, basic, bearer, API key in header, API key in query`) without breaking existing Phase 4 provider fixtures, tests, or `.down.sql` migrations.

## DEC-005: HTTP 5xx and Post-Write I/O Failures Classify Strictly as `SendStatusUnknown`
- **Decision**: Using `httptrace.ClientTrace.WroteRequest`, any timeout, EOF, or network I/O error occurring after HTTP request bytes were written to the upstream socket, as well as any HTTP `5xx` status code (unless explicitly listed in `ThrottleErrorCodes`/`PermanentErrorCodes`), returns `messaging.SendStatusUnknown`. Pre-write TCP dial failures classify as `SendStatusRetryableReject` (failover-safe), while SSRF or redirect violations classify as `SendStatusFatalReject`.
- **Rationale**: Prevents duplicate SMS sends on ambiguous upstream outcomes, honoring the core idempotency/no-duplicate-send invariant across both SMPP and HTTP connectors.

## DEC-006: HTTP Long Message Segmentation Strategy
- **Decision**: Multi-part segmentation (GSM-7 / UCS-2, 153 / 67 chars per part) remains upstream provider/dispatcher compatible: `Service.Submit` segments customer HTTP API submissions into `message_parts` and dispatches per segment (or passes `{{udh}}`, `{{part_number}}`, `{{total_parts}}` in HTTP templates). On the customer SMPP ingress server, pre-split UDH parts remain rejected while single messages and `message_payload` TLV up to max parts are accepted and segmented by Znero.
- **Rationale**: Keeps GSM-7/UCS-2 billing accuracy identical across HTTP and SMPP upstreams without silently charging mismatched part counts; customer-split SMPP UDH concatenation stays explicitly flagged as a manager open question.

## DEC-007: Dial-Time SSRF `net.Dialer.Control` Placement & Dev Loopback
- **Decision**: Added `net.Dialer.Control` IP validation inside shared `smpp.SafeDialer.DialContext` so both SMPP clients, HTTP provider connectors, and customer webhook dispatchers execute dial-time IP validation before `connect()`. `AllowLoopback` is enabled exclusively when `APP_ENV=development` (and integration test suites) for local mock servers (`SMPPMockServer`, `FakeHTTPProvider`), while cloud metadata (`169.254.0.0/16`) and RFC1918 ranges remain unconditionally blocked.
- **Rationale**: Eliminates DNS-rebinding TOCTOU windows uniformly across every egress component in one verified helper.

## DEC-008: Partition-Pruned HTTP DLR Polling Query
- **Decision**: `httpprovider.DLRPoller` scopes active `submitted` polling lookups with `AND created_at >= $since` (8-day sliding window) rather than scanning unbounded `messages` partitions.
- **Rationale**: Ensures PostgreSQL partition pruning always activates on the monthly `messages` partitions even under high multi-month message volumes.

## DEC-009: Admin UI Dev Simulation Toggle for `test-send`
- **Decision**: `POST /api/v1/providers/:id/connections/:connId/test-send` supports executing against the connection's real `http_base_url` AND, only when `APP_ENV=development`, an optional `simulate_mode` (`accept` | `reject` | `throttle` | `server_err`) that launches an ephemeral in-process HTTP server behaving like `FakeHTTPProvider`.
- **Rationale**: Allows offline Admin UI verification in staging/local/Playwright runs through the exact `httpprovider.Client` template/auth/parser stack without weakening SSRF rules in production.

## DEC-010: `on_provider_accept` + `failed_after_accept_policy = 'refund'` Behavior on No-DLR Expiration
- **Decision**: When `Reaper.reapNoDLRMessages` expires an `on_provider_accept` message whose snapshotted `failed_after_accept_policy_snapshot == 'refund'` past `dlr_wait_hours`, it refunds the captured hold via `RefundCaptureTx` (falling back to `ReleaseTx` if uncaptured) and records an `on_provider_accept_nodlr_refund` settlement exception; for `'keep_charge'`, the captured hold is preserved cleanly.
- **Rationale**: Aligns `Reaper` with `ReconciliationService` (`reconcile.go` lines 118–120 & 155), which permits captured holds on `expired` messages ONLY under `keep_charge` or `on_delivery + capture_at_ttl`, preventing permanent critical reconciliation mismatches when carriers omit DLRs.


## DEC-011: `smpp_long_message_mode` Kept on Explicit `reject_split` Policy
- **Decision**: Customer SMPP ingress (`backend/internal/smppserver/session.go`) continues enforcing `reject_split` (`StatusRInvPdu` / explicit rejection when customers send pre-split UDH/SAR concatenated segments) while accepting single short messages (`<= 160` GSM-7 / `<= 70` UCS-2) and `message_payload` TLV (`0x0424`) up to configured max parts, deferring optional per-account `reassemble` mode to a dedicated post-Phase-8 migration (`000015`).
- **Rationale**: Preserves deterministic per-segment billing, prevents partial multipart orphan holds under crash recovery, and avoids adding speculative schema migrations beyond Migration 14 without commercial sign-off.

## DEC-012: Phase 9 Default Commercial Policies Affirmation
- **Decision**: In alignment with Stage 0 requirements:
  1. Submission-billed (`on_provider_accept`) messages that later report FAILED are NOT refunded automatically (`failed_after_accept_policy = 'keep_charge'`).
  2. DLR-billed (`on_delivery`) messages with no receipt release the pre-funding hold after the customer's configured `dlr_wait_hours` + resolution window and record an exposure flag (`no_dlr_policy = 'retain_until_resolution'`).
  3. SMPP customer pre-split (UDH/SAR) messages remain rejected (`reject_split`) until explicit reassembly is commissioned.
- **Rationale**: Reflects standard wholesale carrier liability practice and prevents unhedged upstream financial exposure.

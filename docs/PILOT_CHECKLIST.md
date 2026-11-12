# Znero International — External Provider Pilot Pre-Flight Checklist (`docs/PILOT_CHECKLIST.md`)

This operational checklist must be followed by platform operators before routing production customer traffic through a newly onboarded upstream SMS provider (SMPP or HTTP REST).

---

## 1. Upstream Provider Provisioning & Configuration

- [ ] **Connection Credentials Secured**
  - Upstream host, port, system ID/token, and password encrypted into database via `secrets.KeyRing`.
  - Credentials must never appear in unencrypted database dumps, log files, or terminal outputs.
- [ ] **Protocol Verification**
  - **SMPP (v3.4)**: Verify bind mode (`transceiver`, `transmitter`, or `receiver`), system type, interface version (`0x34`), address ton/npi, and enquire_link interval (standard 30s).
  - **HTTP Provider**: Verify webhook URL, authentication method (Bearer token / Basic / Custom Header), response template parsing (e.g. JSON extraction of provider message ID), and incoming callback DLR HMAC signature secret.
- [ ] **DLR / Delivery Receipt Configuration**
  - Confirm provider DLR callback format matches registered mapping in `provider_dlr_mappings`.
  - Ensure status mapping converts provider statuses (`DELIVRD`, `UNDELIV`, `EXPIRED`, `REJECTD`, `UNKNOWN`) to internal Znero canonical statuses.
  - Verify callback URL is reachable externally and protected by TLS.

---

## 2. Platform Safeguards & Commercial Policies

- [ ] **Operator Emergency Kill Switch Tested**
  - Test immediate engagement: `POST /api/v1/routing/connections/:id/disable` with reason.
  - Confirm in-flight routing candidates immediately exclude the disabled connection.
  - Confirm connection override produces an immutable audit record (`routing.killswitch.disable`).
  - Verify re-enable requires maker-checker approval workflow.
- [ ] **Rate Limiting & Concurrency Controls**
  - Set maximum TPS limit (`max_tps`) on provider connection matching contracted upstream SLA.
  - Set window limit (`max_in_flight` / window size) to prevent upstream buffer overflows.
- [ ] **Pricing & Margin Bounds**
  - Verify buy rate active in `provider_rates` table for all pilot destination MCC/MNCs.
  - Confirm customer sell rates satisfy minimum margin requirements (`sell_rate >= buy_rate + min_margin`).
  - Confirm currency matches customer wallet or note where conversion is not supported.

---

## 3. Pre-Pilot Staging & Verification

- [ ] **Single Synthetic Message Probe**
  - Submit single test SMS through customer edge API to internal test number.
  - Inspect message lifecycle: `accepted -> submitting -> submitted -> delivered`.
  - Confirm provider message ID is recorded and searchable.
  - Confirm financial hold was placed at authorization and captured upon delivery report.
- [ ] **Multi-Part Concatenation Probe**
  - Send 2-part and 3-part GSM-7 and UCS-2 messages.
  - Verify correct segment calculation and UDH header transmission.
  - Confirm multi-part pricing holds and captures correctly.
- [ ] **Failure & Ambiguous Outcome Probe**
  - Simulate network timeout or mock ambiguous reject: confirm message transitions to `unknown` without auto-retry.
  - Confirm DLR reaper handles unacknowledged messages within customer's configured `dlr_wait_hours`.

---

## 4. Live Pilot Ramp-Up Procedure

1. **Initial Micro-Allocation (1-5% of route traffic)**:
   - Assign low route priority or weight to new provider connection in routing plan.
   - Monitor real-time throughput and error rate via `/metrics` and Live Hub.
2. **Telemetry & SLO Monitoring**:
   - Verify `znero_outbox_oldest_age_seconds < 10s`.
   - Verify `znero_reconciliation_discrepancies_total == 0`.
   - Monitor DLR latency percentiles (p50, p95, p99).
3. **Emergency Rollback & Drain Plan**:
   - If error rate exceeds 2%, immediately trigger connection kill switch via admin dashboard.
   - Verify outbox safely diverts remaining traffic to secondary carrier route.
   - Run reconciliation job: `POST /api/v1/messaging/reconciliation/run` and verify `0` discrepancies.

# SMPP Protocol Engineering Notes & Specification (Phase 8A Chunk 2)

## 1. Library Evaluation and Architecture Decision

### Decision: Native Clean-Room SMPP 3.4 PDU Codec & Session Engine
The initial architectural design considered `github.com/linxGnu/gosmpp`. A rigorous technical evaluation was conducted against the mission-critical requirements of Znero International:

| Critical Requirement | `gosmpp` Evaluation | Custom PDU Codec (`internal/smpp/pdu`) |
| :--- | :--- | :--- |
| **Dial-Time SSRF Validation** | **Incompatible**: Dial logic is buried inside opaque session constructors without pre-dial IP resolution & validation hooks. | **Supported**: Custom `SafeDialer` resolves DNS, verifies every IP against blocked RFC 1918 / 3927 / carrier NAT CIDRs, and dials validated IP directly. |
| **Strict TLS 1.2+ Hostname Verification** | Limited control over SNI server name when dialing resolved IP addresses. | **Supported**: Explicit SNI preservation with `tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}`. |
| **Sliding Window Flow Control** | Fixed buffer internal queues; decoupled from database connection configuration. | **Supported**: Sliding window semaphore channel directly sized to `provider_connections.smpp_window_size`. |
| **Context Cancellation & Timeout Semantics** | Submissions do not cleanly cancel in-flight network writes without tearing down the entire connection session. | **Supported**: Context timeouts cleanly release sliding window tokens and immediately return `SendStatusUnknown`. |
| **Zero External Supply Chain Risk** | Brings third-party transitive dependencies. | **Supported**: Pure Go standard library (`encoding/binary`, `crypto/tls`, `net`, `sync`). Zero CVE / supply chain risk. |
| **Dual-Use Codec** | Client-centric; difficult to adapt for high-fidelity mock SMSC simulator. | **Supported**: Exactly identical PDU codec powers both the egress SMPP client and the mock simulator. |

---

## 2. SMPP 3.4 Status Code Classification Table

Carrier responses from `submit_sm_resp` are mapped into state transitions according to SMPP 3.4:

| Status Code | Hex | SMPP 3.4 Description | Znero Classification (`SendStatus`) | Action Taken |
| :--- | :--- | :--- | :--- | :--- |
| `ESME_ROK` | `0x00000000` | No Error | `SendStatusAccepted` | Recorded in `message_provider_lookups`; message moves to `StatusSubmitted` (`PROVIDER_ACCEPTED`). |
| `ESME_RTHROTTLED` | `0x00000058` | ESME Throttled | `SendStatusRetryableReject` | Triggers failover retry on next available route via `routing.Engine.Next()`. |
| `ESME_RMSGQFUL` | `0x00000014` | Message Queue Full | `SendStatusRetryableReject` | Triggers failover retry on next available route. |
| `ESME_RSYSERR` | `0x00000008` | System Error | `SendStatusRetryableReject` | Triggers failover retry on next available route. |
| `ESME_RBINDFAIL` | `0x0000000D` | Bind Failed | `SendStatusRetryableReject` | Triggers session reconnection with exponential backoff & failover. |
| `ESME_RINVPASWD` | `0x0000000E` | Invalid Password | `SendStatusRetryableReject` | Triggers authentication backoff cap & alert. |
| `ESME_RINVSYSID` | `0x0000000F` | Invalid System ID | `SendStatusRetryableReject` | Triggers authentication backoff cap & alert. |
| `ESME_RINVBNDSTS` | `0x00000004` | Incorrect Bind Status | `SendStatusRetryableReject` | Re-establishes session bind; fails over current message. |
| `ESME_RALYBND` | `0x00000005` | Already in Bound State | `SendStatusRetryableReject` | Handled by session manager; advisory lock prevents duplicates. |
| `ESME_RINVDSTADR` | `0x0000000B` | Invalid Destination Address | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold via `ReleaseTx` in same transaction. **No failover**. |
| `ESME_RINVSRCADR` | `0x0000000A` | Invalid Source Address | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |
| `ESME_RINVMSGLEN` | `0x00000001` | Message Length Invalid | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |
| `ESME_RINVDCS` | `0x00000063` | Invalid Data Coding Scheme | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |
| `ESME_RINVPRTFLG` | `0x00000006` | Invalid Priority Flag | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |
| `ESME_RINVREGDLVFLG`| `0x00000007` | Invalid Registered Delivery | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |
| `ESME_RINVOPTPARVAL`| `0x000000C4` | Invalid Optional Parameter | `SendStatusFatalReject` | **Permanent failure**: moves message to `StatusFailed`, releases ledger hold. **No failover**. |

---

## 3. Submission Outcome Rules & Ambiguity Handling

1. **Definitive Success (`ESME_ROK`)**:
   - Upstream carrier has accepted responsibility for message delivery.
   - Provider message ID stored in unpartitioned `message_provider_lookups` inside the transaction moving message to `StatusSubmitted`.
2. **Definitive Retryable Failure (`throttled`, `queue full`, `system error`)**:
   - Upstream carrier has explicitly refused the message with a retryable code.
   - Outbox dispatcher invokes `routing.Engine.Next()`, excluding previously attempted connections, and attempts the next qualified carrier route.
3. **Definitive Permanent Failure (`invalid destination`, `invalid sender ID`, `bad length`)**:
   - Upstream carrier has permanently rejected the message.
   - The message moves to `StatusFailed`.
   - The ledger hold is released immediately via `ReleaseTx` in the **exact same database transaction** as the status update.
4. **Ambiguous Outcome / Network Timeout / Dropped Socket**:
   - If a network timeout, socket reset, or worker restart occurs **after** the PDU bytes have been written to the socket, the outcome is classified strictly as **`SendStatusUnknown`**.
   - **CRITICAL**: The message is **NEVER** resent or failed over. Resending would risk duplicate message delivery and duplicate billing.
   - The customer wallet hold is strictly retained. Final resolution is deferred to delivery receipt reconciliation or the messaging reaper job in Chunk 3.

---

## 4. Multi-Part Message Aggregation Policy

When a message exceeds single-PDU capacity (160 characters GSM-7 or 70 characters UCS-2), it is segmented into multiple parts using standard 6-byte User Data Headers (UDH) with 8-bit reference numbers:
- `0x05` (UDH Length)
- `0x00` (Information Element: Concatenated SMS)
- `0x03` (Header Length)
- `ref` (Reference Number)
- `total` (Total Parts)
- `seq` (Part Sequence)

The overall message state is aggregated from part outcomes using the following strict hierarchy:
1. **Fatal Priority**: If **any** part receives a fatal rejection (`SendStatusFatalReject`), the entire message is marked `StatusFailed`. The hold is released.
2. **Unknown Priority**: If **any** part experiences a timeout or socket drop (`SendStatusUnknown`), the entire message is marked `StatusUnknown`. The hold is retained; no retries occur.
3. **Retryable Priority**: If **any** part receives a retryable error (`SendStatusRetryableReject`) and no parts are fatal/unknown, the outbox retries failover for remaining parts.
4. **All Accepted**: Only when **all** parts receive `ESME_ROK` (`SendStatusAccepted`) does the message transition to `StatusSubmitted`.

---

## 5. DLR Ingestion Policy in Chunk 2 & Settlement in Chunk 3

When an incoming `deliver_sm` is received on a bound transceiver/receiver session:
1. The raw PDU payload, TLVs, and headers are extracted.
2. The receipt is durably inserted into `message_dlrs` **prior** to transmitting `deliver_sm_resp` (`StatusROK`).
3. In Chunk 3, delivery receipts are matched against `message_provider_lookups` using dual representation candidates (raw ID, hex-to-decimal, decimal-to-hex, and leading-zero stripped).
4. If no message is matched within the transaction, the raw receipt is preserved in `unmatched_dlrs` for periodic background reconciliation.
5. Once matched, state transitions obey strict monotonic order: terminal states (`delivered`, `undelivered`, `expired`, `failed`, `rejected`) can never transition backwards. Conflicting out-of-order receipts trigger an exception in `settlement_exceptions`.
6. Ledger settlement occurs in the exact same transaction:
   - For `billing_trigger = 'on_delivery'`: upon `delivered`, `ledgerSvc.CaptureTx` captures the reserved hold.
   - For `billing_trigger = 'on_submission'`: the hold was captured at submission time.
   - Upon definitive delivery failure (`undelivered`, `expired`, `failed`, `rejected`): `ledgerSvc.ReleaseTx` releases the hold if not already captured.
   - For `StatusUnknown` messages: arrival of a matching `delivered` or `undelivered` DLR cleanly resolves the ambiguous state.


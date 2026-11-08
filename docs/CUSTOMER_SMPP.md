# Znero International Customer SMPP 3.4 Gateway Specification

The Znero Customer SMPP Gateway provides high-throughput carrier-grade connectivity for customers integrating SMS submission and receiving real-time delivery receipts and inbound messages via the Short Message Peer-to-Peer (SMPP) v3.4 protocol.

---

## 1. Connection Parameters

| Parameter | Default Value | Notes |
| :--- | :--- | :--- |
| **Host** | `smpp.znero.io` | Domain or IP address provided in onboarding |
| **Port (TCP)** | `2776` | Standard unencrypted SMPP 3.4 |
| **Port (TLS)** | `3550` | Encrypted SMPP 3.4 over TLS 1.3 |
| **Version** | `0x34` (SMPP 3.4) | Standard SMPP version byte |
| **Keepalive** | `enquire_link` | Recommended every 30 seconds |
| **Idle Timeout** | 60 seconds | Unacknowledged connections closed automatically |

---

## 2. Authentication & Bind Rules

Authentication is verified via the `bind_transmitter`, `bind_receiver`, or `bind_transceiver` PDUs.

- **System ID (`system_id`)**: Case-sensitive customer username (max 16 chars).
- **Password (`password`)**: Verified against the platform's Argon2id password hash in constant time.
- **Allowed Bind Types**: Customers can be restricted to `transmitter`, `receiver`, or `transceiver` binds.
- **Concurrent Binds (`max_binds`)**: Enforced strictly per account (typically 2â€“8 binds). Excessive binds are rejected with `ESME_RBINDFAIL` (0x0000000D).
- **IP Allowlisting**: Binds originating from unauthorized client IP addresses are rejected with `ESME_RBINDFAIL`.

---

## 3. Submission Rules (`submit_sm`)

### 3.1 Single PDU Enforcement
To preserve deterministic pricing, atomic rate checks, and routing integrity:
- **No UDH Concatenation**: `esm_class` bit 6 (`0x40`) is prohibited.
- **No SAR Fragmentation**: SAR TLVs (`sar_msg_ref_num`, `sar_total_segments`, `sar_segment_seqnum`) are prohibited.
- Messages exceeding standard PDU size are rejected with `ESME_RINVMSGLEN` (0x00000001). Customers submitting long messages must split them at the application layer or use the Customer REST API.

### 3.2 Transactional Confirmation
The gateway does **NOT** emit `submit_sm_resp` until the message record, financial hold, and routing assignment have been durably committed to the database.

### 3.3 Message ID Format
The returned `message_id` in `submit_sm_resp` is a 32-character hexadecimal string without hyphens, matching the platform's UUID:
```
c1f7a28e3b4d456789abcdef01234567
```

---

## 4. Delivery Receipts (DLR)

Customers connected via `bind_receiver` or `bind_transceiver` receive delivery receipts via `deliver_sm` with `esm_class = 0x04`.

### 4.1 Receipt Text Format
The `short_message` field contains the industry-standard ASCII delivery receipt:
```text
id:c1f7a28e3b4d456789abcdef01234567 sub:001 dlvrd:001 submit date:2611090200 done date:2611090200 stat:DELIVRD err:000 text:
```

### 4.2 Standard TLVs
For modern SMPP clients, the gateway also populates standard TLVs:
- `receipted_message_id` (`0x001E`): Null-terminated 32-hex message reference.
- `message_state` (`0x0427`): 1-byte state integer:
  - `2`: `DELIVERED`
  - `3`: `EXPIRED`
  - `5`: `UNDELIVERABLE`
  - `8`: `REJECTED`

Clients must acknowledge receipt with `deliver_sm_resp` within 5 seconds.

---

## 5. Inbound (MO) SMS Delivery

Incoming messages sent by mobile subscribers to virtual numbers or shortcodes assigned to the customer are delivered to the customer's active `bind_receiver` or `bind_transceiver` session:
- PDU: `deliver_sm`
- `esm_class`: `0x00` (identifies a standard message, NOT a delivery receipt)
- `source_addr`: Mobile subscriber's phone number
- `dest_addr`: Customer's assigned virtual number
- `short_message`: Content of the incoming SMS (or UCS-2 encoded if `data_coding = 0x08`)

---

## 6. Throughput & Flow Control

- **Token Bucket Rate Limiting**: The customer's TPS limit applies jointly across all active sessions. If exceeded, `submit_sm_resp` returns `ESME_RTHROTTLED` (0x00000058).
- **Sliding Window**: Sessions support up to 16 concurrent in-flight PDUs.
- **Enquire Link**: Either peer may send `enquire_link` at any time to verify transport liveness. The receiving peer must reply with `enquire_link_resp` immediately.

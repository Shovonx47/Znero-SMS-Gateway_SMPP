# Znero International Customer REST API Specification

The Customer Edge REST API provides programmatic SMS submission, real-time status queries, pricing estimation, wallet balance inspection, inbound SMS consumption, and webhook configuration.

---

## 1. Authentication & Security

All API endpoints are hosted under:
```
https://<domain>/api/v1/customer
```

### Bearer Token Authentication
Requests require a standard `Authorization` header containing an active API key:
```http
Authorization: Bearer zn_live_sample_key_1234567890
```

- **Storage & Validation**: Raw keys are never stored in plaintext. The platform stores a cryptographic SHA-256 hash alongside a non-secret key prefix (`zn_live_...`). Incoming keys are validated in constant time with a 10-second in-memory authentication cache.
- **IP Allowlisting**: Keys can optionally restrict inbound calls to explicit CIDR ranges or IPv4/IPv6 addresses.
- **Brute-Force Lockout**: Inbound requests failing authentication 10 consecutive times within 60 seconds trigger a temporary 5-minute lockout for the offending client IP.

### Permission Scopes
API keys are scoped with least-privilege permissions:
- `sms:send`: Required for `POST /sms` and `POST /sms/bulk`.
- `sms:read`: Required for `GET /sms/{id}` and `GET /sms`.
- `sms:read_content`: Required to view unmasked message body texts.
- `account:read`: Required for `GET /balance`, `GET /rates`, `GET /account`, and webhook CRUD.
- `inbound:read`: Required for `GET /inbound`.

---

## 2. Rate Limits & Token Bucket Throughput

Each customer account is assigned a maximum Transaction-Per-Second limit (`max_tps`).
- Throughput is managed via an in-memory token bucket rate limiter.
- If the token bucket is exhausted, calls immediately return `429 Too Many Requests` with a `Retry-After: 1` header.
- Bulk requests (`POST /sms/bulk`) perform an upfront atomic token reservation: submitting a batch of 100 messages consumes 100 tokens. If fewer than 100 tokens remain in the bucket, the entire batch is rejected with `429 Too Many Requests` without creating partial database state.

---

## 3. Core API Endpoints

### 3.1 Single SMS Submission
`POST /api/v1/customer/sms`
Scope required: `sms:send`

#### Request Body
```json
{
  "to": "+14155550199",
  "from": "ZNERO",
  "text": "Your verification code is 881920",
  "client_reference": "order-88192-step1",
  "validity_seconds": 86400
}
```

| Parameter | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `to` | string | Yes | Destination mobile number in E.164 format (e.g. `+14155550199`). |
| `from` | string | No | Sender ID or virtual number (default: account default). Max 11 alphanumeric or 15 numeric digits. |
| `text` | string | Yes | Message body content (UTF-8). |
| `client_reference` | string | No | Customer idempotency key (max 64 characters). Retries return original message without duplicate billing. |
| `validity_seconds` | int | No | Message time-to-live in seconds (min 60, max 604800, default 86400). |

#### Response (`201 Created` or `200 OK` on replay)
```json
{
  "id": "c1f7a28e3b4d456789abcdef01234567",
  "status": "queued",
  "to": "+14155550199",
  "from": "ZNERO",
  "segments": 1,
  "encoding": "GSM-7",
  "price": "0.007500",
  "currency": "USD",
  "client_reference": "order-88192-step1",
  "created_at": "2026-11-09T02:00:00Z"
}
```

---

### 3.2 Bulk SMS Submission
`POST /api/v1/customer/sms/bulk`
Scope required: `sms:send`

Upfront token reserve. Accepts up to 1000 messages in a single atomic request.

#### Request Body
```json
{
  "messages": [
    {
      "to": "+14155550199",
      "from": "ZNERO",
      "text": "Broadcast message 1",
      "client_reference": "bulk-001"
    },
    {
      "to": "+14155550200",
      "from": "ZNERO",
      "text": "Broadcast message 2",
      "client_reference": "bulk-002"
    }
  ]
}
```

#### Response (`200 OK`)
```json
{
  "total": 2,
  "accepted": 2,
  "rejected": 0,
  "results": [
    {
      "id": "c1f7a28e3b4d456789abcdef01234567",
      "client_reference": "bulk-001",
      "status": "queued",
      "segments": 1
    },
    {
      "id": "d2e8b39f4c5e567890bcdef12345678",
      "client_reference": "bulk-002",
      "status": "queued",
      "segments": 1
    }
  ]
}
```

---

### 3.3 SMS Pricing & Encoding Estimate
`POST /api/v1/customer/sms/estimate`
Scope required: `sms:read`

Calculates segment division, GSM-7 / UCS-2 character classification, and anticipated billing amount without reserving funds or queueing a message.

#### Response (`200 OK`)
```json
{
  "destination": "+14155550199",
  "encoding": "GSM-7",
  "char_count": 32,
  "segment_count": 1,
  "max_chars_per_segment": 160,
  "estimated_price": "0.007500",
  "currency": "USD"
}
```

---

### 3.4 Retrieve Message by ID
`GET /api/v1/customer/sms/{id}`
Scope required: `sms:read`

Accepts either a standard UUID (`550e8400-e29b-41d4-a716-446655440000`) or a 32-hex SMPP message ID format (`550e8400e29b41d4a716446655440000`).

#### Response (`200 OK`)
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "delivered",
  "to": "+14155550199",
  "from": "ZNERO",
  "text": "Your verification code is 881920",
  "segments": 1,
  "encoding": "GSM-7",
  "price": "0.007500",
  "currency": "USD",
  "client_reference": "order-88192-step1",
  "created_at": "2026-11-09T02:00:00Z",
  "delivered_at": "2026-11-09T02:00:03Z"
}
```

---

### 3.5 Account Balance & Limits
`GET /api/v1/customer/balance`
Scope required: `account:read`

Returns live financial balances including held funds and credit limits.

#### Response (`200 OK`)
```json
{
  "currency": "USD",
  "balance": "2450.500000",
  "active_holds": "12.750000",
  "available_balance": "2437.750000",
  "credit_limit": "5000.000000"
}
```

---

### 3.6 Inbound MO Messages
`GET /api/v1/customer/inbound`
Scope required: `inbound:read`

Returns incoming subscriber messages addressed to virtual numbers or shortcodes assigned to the customer.

#### Response (`200 OK`)
```json
{
  "items": [
    {
      "id": "e3a1b2c4-d5e6-7890-abcd-ef1234567890",
      "from": "+14155550188",
      "to": "+14155550199",
      "text": "YES",
      "encoding": "GSM-7",
      "received_at": "2026-11-09T02:15:30Z"
    }
  ],
  "total": 1,
  "page": 1,
  "per_page": 20
}
```

---

## 4. Webhooks Management

Customers can register HTTP endpoints to receive real-time push events.

### Events
- `message.delivered`: Delivery receipt confirmed by handset/carrier.
- `message.failed`: Permanent delivery rejection or downstream timeout.
- `message.inbound`: Inbound SMS received on assigned virtual number.

### Endpoints
- `POST /api/v1/customer/webhooks`: Register endpoint. Returns signing secret **once**.
- `GET /api/v1/customer/webhooks`: List configured webhooks.
- `DELETE /api/v1/customer/webhooks/{id}`: Delete webhook.
- `POST /api/v1/customer/webhooks/{id}/test`: Dispatch synthetic test event.
- `GET /api/v1/customer/webhooks/{id}/deliveries`: View recent dispatch attempts and latencies.

### HMAC-SHA256 Signature Verification
Every webhook delivery contains an `X-Znero-Signature` header:
```http
X-Znero-Signature: t=1720000000,v1=5d41402abc4b2a76b9719d911017c592b23a9d3a7e58e658797f75d55b0a394f
```

To verify the signature:
1. Extract `t` (Unix timestamp) and `v1` (hex signature).
2. Compute HMAC-SHA256 of `<t>.<raw_body_bytes>` using your webhook signing secret.
3. Compare the computed hex signature in constant time against `v1`.

---

## 5. Customer Status Mapping & Error Codes

Internal states are mapped to customer-friendly coarse statuses:

| Customer Status | Internal States | Coarse Code | Description |
| :--- | :--- | :--- | :--- |
| `queued` | `pending`, `hold_placed`, `routed` | `QUEUED` | Accepted, hold placed, awaiting carrier transmission. |
| `sent` | `submitted`, `sending` | `SENT` | Transmitted to downstream carrier SMSC. |
| `delivered` | `delivered` | `DELIVRD` | Terminal delivery confirmed by handset receipt. |
| `failed` | `failed`, `rejected`, `undelivered` | `REJECTD` / `UNDELIV` | Terminal carrier rejection or routing failure. |
| `expired` | `expired` | `EXPIRED` | Message TTL exceeded before delivery confirmation. |

### Standard Error Responses (RFC 7807)
```json
{
  "code": "insufficient_funds",
  "message": "Available balance and credit limit are insufficient for this request",
  "http_status": 402
}
```
Common error codes: `unauthorized`, `forbidden_scope`, `rate_limited`, `invalid_phone_number`, `unroutable_destination`, `insufficient_funds`, `idempotency_conflict`.

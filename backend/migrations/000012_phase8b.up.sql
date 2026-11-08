-- Migration 000012: Phase 8B Customer Edge, SMPP Server, Webhooks, and Inbound MO

-- 1. Idempotency Request Fingerprint
ALTER TABLE message_idempotency_lookups 
    ADD COLUMN IF NOT EXISTS request_fingerprint VARCHAR(64);

-- 2. Message Edge Metadata & Routing Links
ALTER TABLE messages 
    ADD COLUMN IF NOT EXISTS ingress_channel VARCHAR(32) NOT NULL DEFAULT 'platform',
    ADD COLUMN IF NOT EXISTS ingress_account_id UUID,
    ADD COLUMN IF NOT EXISTS dlr_requested BOOLEAN NOT NULL DEFAULT false;

-- 3. Unpartitioned Message Locator (High-speed single-index lookup avoiding all-partition scans)
CREATE TABLE IF NOT EXISTS message_locator (
    message_id         UUID PRIMARY KEY,
    created_at         TIMESTAMPTZ NOT NULL,
    customer_org_id    UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_message_locator_cust_created 
    ON message_locator (customer_org_id, created_at DESC);

-- Populate locator for any existing messages
INSERT INTO message_locator (message_id, created_at, customer_org_id)
SELECT id, created_at, customer_org_id FROM messages
ON CONFLICT (message_id) DO NOTHING;

-- 4. Customer Usage Counters (Atomic daily and monthly quota enforcement)
CREATE TABLE IF NOT EXISTS customer_usage_counters (
    customer_org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    period_type     VARCHAR(16) NOT NULL, -- 'day', 'month'
    period_start    TIMESTAMPTZ NOT NULL,
    message_count   BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_customer_usage_counters PRIMARY KEY (customer_org_id, period_type, period_start),
    CONSTRAINT chk_period_type CHECK (period_type IN ('day', 'month'))
);

-- 5. Customer SMPP Sessions Persistence
CREATE TABLE IF NOT EXISTS customer_smpp_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES customer_smpp_accounts(id) ON DELETE CASCADE,
    customer_org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    remote_ip       VARCHAR(64) NOT NULL,
    bind_type       VARCHAR(32) NOT NULL, -- 'transceiver', 'transmitter', 'receiver'
    system_id       VARCHAR(64) NOT NULL,
    bound_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity   TIMESTAMPTZ NOT NULL DEFAULT now(),
    in_flight       INT NOT NULL DEFAULT 0,
    messages_sent   BIGINT NOT NULL DEFAULT 0,
    dlrs_delivered  BIGINT NOT NULL DEFAULT 0,
    closed_at       TIMESTAMPTZ,
    close_reason    VARCHAR(255)
);
CREATE INDEX IF NOT EXISTS idx_customer_smpp_sessions_active 
    ON customer_smpp_sessions (account_id, closed_at);
CREATE INDEX IF NOT EXISTS idx_customer_smpp_sessions_customer 
    ON customer_smpp_sessions (customer_org_id, bound_at DESC);

-- 6. SMPP Customer DLR Queue (At-least-once deliver_sm queue)
CREATE TABLE IF NOT EXISTS smpp_dlr_queue (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id        UUID NOT NULL REFERENCES customer_smpp_accounts(id) ON DELETE CASCADE,
    message_id        UUID NOT NULL,
    message_ref       VARCHAR(64) NOT NULL, -- 32 hex chars message ID for SMPP
    client_reference  VARCHAR(128),
    destination       VARCHAR(64) NOT NULL,
    sender_id         VARCHAR(64) NOT NULL,
    status            VARCHAR(32) NOT NULL, -- 'delivered', 'undelivered', 'expired', 'failed', 'rejected'
    error_code        VARCHAR(16) NOT NULL DEFAULT '000',
    payload           JSONB NOT NULL,
    attempts          INT NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_until      TIMESTAMPTZ,
    leased_by         VARCHAR(64),
    queue_status      VARCHAR(32) NOT NULL DEFAULT 'pending', -- 'pending', 'delivered', 'expired'
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at      TIMESTAMPTZ,
    CONSTRAINT chk_smpp_dlr_status CHECK (queue_status IN ('pending', 'delivered', 'expired'))
);
CREATE INDEX IF NOT EXISTS idx_smpp_dlr_queue_pending 
    ON smpp_dlr_queue (account_id, queue_status, next_attempt_at) 
    WHERE queue_status = 'pending';

-- 7. Customer Webhooks & History (Encrypted signing secrets, write-only)
CREATE TABLE IF NOT EXISTS customer_webhooks (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    url                  TEXT NOT NULL,
    secret_encrypted     TEXT NOT NULL,
    secret_key_id        VARCHAR(64) NOT NULL,
    events               JSONB NOT NULL DEFAULT '["message.final"]'::jsonb,
    status               VARCHAR(32) NOT NULL DEFAULT 'active', -- 'active', 'disabled', 'disabled_auto'
    consecutive_failures INT NOT NULL DEFAULT 0,
    version              INT NOT NULL DEFAULT 1,
    created_by           UUID REFERENCES users(id),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_webhook_status CHECK (status IN ('active', 'disabled', 'disabled_auto'))
);
CREATE INDEX IF NOT EXISTS idx_customer_webhooks_cust 
    ON customer_webhooks (customer_org_id, status);

CREATE TABLE IF NOT EXISTS customer_webhooks_history (
    history_id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id           UUID NOT NULL REFERENCES customer_webhooks(id) ON DELETE CASCADE,
    customer_org_id      UUID NOT NULL,
    url                  TEXT NOT NULL,
    events               JSONB NOT NULL,
    status               VARCHAR(32) NOT NULL,
    consecutive_failures INT NOT NULL,
    version              INT NOT NULL,
    changed_by           UUID REFERENCES users(id),
    changed_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason        VARCHAR(255)
);
CREATE INDEX IF NOT EXISTS idx_customer_webhooks_hist 
    ON customer_webhooks_history (webhook_id, version DESC);

-- 8. Customer Webhook Deliveries & Execution Attempts
CREATE TABLE IF NOT EXISTS customer_webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID NOT NULL REFERENCES customer_webhooks(id) ON DELETE CASCADE,
    customer_org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    event_id        VARCHAR(64) NOT NULL,
    event_type      VARCHAR(64) NOT NULL,
    payload         JSONB NOT NULL,
    status          VARCHAR(32) NOT NULL DEFAULT 'pending', -- 'pending', 'success', 'failed'
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_until    TIMESTAMPTZ,
    leased_by       VARCHAR(64),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    CONSTRAINT chk_webhook_delivery_status CHECK (status IN ('pending', 'success', 'failed'))
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending 
    ON customer_webhook_deliveries (status, next_attempt_at) 
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_cust 
    ON customer_webhook_deliveries (customer_org_id, created_at DESC);

CREATE TABLE IF NOT EXISTS customer_webhook_attempts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    delivery_id      UUID NOT NULL REFERENCES customer_webhook_deliveries(id) ON DELETE CASCADE,
    attempt_number   INT NOT NULL,
    response_code    INT,
    latency_ms       INT NOT NULL,
    response_snippet TEXT,
    error_class      VARCHAR(64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_webhook_attempts_delivery 
    ON customer_webhook_attempts (delivery_id, created_at DESC);

-- 9. Inbound Numbers Registry & History (Platform Provisioned, Maker-Checker on Assignments)
CREATE TABLE IF NOT EXISTS inbound_numbers (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number_raw             VARCHAR(64) NOT NULL,
    number_e164            VARCHAR(64) NOT NULL,
    country_id             UUID REFERENCES countries(id),
    provider_connection_id UUID REFERENCES provider_connections(id),
    customer_org_id        UUID REFERENCES organizations(id) ON DELETE SET NULL,
    type                   VARCHAR(32) NOT NULL DEFAULT 'longcode', -- 'longcode', 'shortcode'
    status                 VARCHAR(32) NOT NULL DEFAULT 'active', -- 'active', 'inactive'
    capabilities           JSONB NOT NULL DEFAULT '["sms"]'::jsonb,
    version                INT NOT NULL DEFAULT 1,
    created_by             UUID REFERENCES users(id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_inbound_numbers_e164 UNIQUE (number_e164),
    CONSTRAINT chk_inbound_number_status CHECK (status IN ('active', 'inactive'))
);
CREATE INDEX IF NOT EXISTS idx_inbound_numbers_cust 
    ON inbound_numbers (customer_org_id) 
    WHERE customer_org_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS inbound_numbers_history (
    history_id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inbound_number_id      UUID NOT NULL REFERENCES inbound_numbers(id) ON DELETE CASCADE,
    number_raw             VARCHAR(64) NOT NULL,
    number_e164            VARCHAR(64) NOT NULL,
    country_id             UUID,
    provider_connection_id UUID,
    customer_org_id        UUID,
    status                 VARCHAR(32) NOT NULL,
    version                INT NOT NULL,
    changed_by             UUID REFERENCES users(id),
    changed_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason          VARCHAR(255)
);
CREATE INDEX IF NOT EXISTS idx_inbound_numbers_hist 
    ON inbound_numbers_history (inbound_number_id, version DESC);

-- 10. Inbound Messages Table (Partitioned Monthly, Encrypted Body)
CREATE TABLE IF NOT EXISTS inbound_messages (
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    source_raw             VARCHAR(64) NOT NULL,
    source_e164            VARCHAR(64) NOT NULL,
    destination_raw        VARCHAR(64) NOT NULL,
    destination_e164       VARCHAR(64) NOT NULL,
    inbound_number_id      UUID REFERENCES inbound_numbers(id),
    customer_org_id        UUID REFERENCES organizations(id),
    provider_connection_id UUID REFERENCES provider_connections(id),
    provider_message_id    VARCHAR(128),
    encrypted_body         BYTEA NOT NULL,
    key_id                 VARCHAR(64) NOT NULL,
    encoding               VARCHAR(32) NOT NULL DEFAULT 'GSM7',
    concat_ref             INT,
    concat_part            INT,
    concat_total           INT,
    dedupe_hash            VARCHAR(64) NOT NULL,
    status                 VARCHAR(32) NOT NULL DEFAULT 'received', -- 'received', 'routed', 'unrouted', 'delivered'
    CONSTRAINT pk_inbound_messages PRIMARY KEY (id, created_at),
    CONSTRAINT chk_inbound_msg_status CHECK (status IN ('received', 'routed', 'unrouted', 'delivered'))
) PARTITION BY RANGE (created_at);

-- Initial Monthly Partitions
CREATE TABLE IF NOT EXISTS inbound_messages_2026_11 PARTITION OF inbound_messages
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');
CREATE TABLE IF NOT EXISTS inbound_messages_2026_12 PARTITION OF inbound_messages
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');
CREATE TABLE IF NOT EXISTS inbound_messages_2027_01 PARTITION OF inbound_messages
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');
CREATE TABLE IF NOT EXISTS inbound_messages_default PARTITION OF inbound_messages DEFAULT;

CREATE INDEX IF NOT EXISTS idx_inbound_messages_cust 
    ON inbound_messages (customer_org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_inbound_messages_dedupe 
    ON inbound_messages (destination_e164, dedupe_hash, created_at DESC);

-- 11. Update change_requests Entity Constraint for Inbound Numbers & Webhooks
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable',
        'system_setting', 'dlr_mapping',
        'inbound_number', 'customer_webhook'
    ));

-- 12. Edge & Public API Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
  ('customer.webhook.create', 'customer', 'webhook', 'create', 'Create customer webhook subscriptions'),
  ('customer.webhook.read', 'customer', 'webhook', 'read', 'View customer webhook configurations and delivery logs'),
  ('customer.webhook.update', 'customer', 'webhook', 'update', 'Update webhook endpoints, rotate signing secrets and send test events'),
  ('customer.webhook.delete', 'customer', 'webhook', 'delete', 'Delete customer webhook subscriptions'),
  ('customer.usage.view', 'customer', 'usage', 'view', 'View customer rate limits and daily/monthly quota usage'),
  ('customer.smppsession.view', 'customer', 'smppsession', 'view', 'View real-time customer SMPP bind sessions'),
  ('customer.smppsession.disconnect', 'customer', 'smppsession', 'disconnect', 'Force disconnect customer SMPP sessions'),
  ('inbound.number.create', 'inbound', 'number', 'create', 'Provision inbound virtual longcodes and shortcodes'),
  ('inbound.number.read', 'inbound', 'number', 'read', 'List and view provisioned inbound numbers'),
  ('inbound.number.update', 'inbound', 'number', 'update', 'Modify inbound number assignment and configuration'),
  ('inbound.number.delete', 'inbound', 'number', 'delete', 'Decommission inbound numbers'),
  ('inbound.message.view', 'inbound', 'message', 'view', 'View received inbound SMS messages')
ON CONFLICT (code) DO NOTHING;

-- Assign permissions to system roles
-- Platform Super Admin and Platform Admin get everything
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Super Admin', 'Platform Admin')
  AND p.code IN (
      'customer.webhook.create', 'customer.webhook.read', 'customer.webhook.update', 'customer.webhook.delete',
      'customer.usage.view', 'customer.smppsession.view', 'customer.smppsession.disconnect',
      'inbound.number.create', 'inbound.number.read', 'inbound.number.update', 'inbound.number.delete',
      'inbound.message.view'
  )
ON CONFLICT DO NOTHING;

-- Customer Org Admin gets self-service webhook management, usage view, and inbound message view
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'customer'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'customer.webhook.create', 'customer.webhook.read', 'customer.webhook.update', 'customer.webhook.delete',
      'customer.usage.view', 'inbound.message.view'
  )
ON CONFLICT DO NOTHING;

-- Reseller Org Admin gets read-only visibility into subtree customer webhooks, usage, and inbound messages
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'reseller'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'customer.webhook.read', 'customer.usage.view', 'inbound.message.view'
  )
ON CONFLICT DO NOTHING;

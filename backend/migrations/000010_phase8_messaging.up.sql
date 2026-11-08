-- Phase 8: Messaging Engine Migration (000010)

-- 1. System Settings Table (Platform Configuration & Maker-Checker Settings)
CREATE TABLE IF NOT EXISTS system_settings (
    key         VARCHAR(128) PRIMARY KEY,
    value       JSONB NOT NULL,
    description TEXT,
    updated_by  UUID REFERENCES users(id),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed customer billing trigger setting (default: on_delivery)
INSERT INTO system_settings (key, value, description)
VALUES ('billing.sms.customer_trigger', '"on_delivery"', 'Customer billing trigger: on_delivery (default) or on_provider_accept')
ON CONFLICT (key) DO NOTHING;

-- Seed hold timing settings
INSERT INTO system_settings (key, value, description)
VALUES 
  ('messaging.message_ttl', '86400', 'Maximum message validity period in seconds (default: 24h)'),
  ('messaging.unknown_resolution_window', '86400', 'Ambiguous outcome resolution window in seconds (default: 24h)'),
  ('messaging.hold_expiry', '259200', 'Customer balance hold expiry in seconds (default: 72h)'),
  ('messaging.partition_precreate_months', '3', 'Number of upcoming monthly partitions to keep pre-created')
ON CONFLICT (key) DO NOTHING;

-- 2. Partitioned Messages Table
CREATE TABLE messages (
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    customer_org_id        UUID NOT NULL REFERENCES organizations(id),
    client_reference       VARCHAR(128),
    original_destination   VARCHAR(64) NOT NULL,
    destination            VARCHAR(64) NOT NULL,
    sender_id              VARCHAR(64) NOT NULL,
    sender_id_type         VARCHAR(32) NOT NULL DEFAULT 'alphanumeric',
    message_type           VARCHAR(32) NOT NULL DEFAULT 'sms',
    encoding               VARCHAR(32) NOT NULL DEFAULT 'GSM7',
    encrypted_body         BYTEA NOT NULL,
    key_id                 VARCHAR(64) NOT NULL,
    body_length            INT NOT NULL DEFAULT 0,
    total_parts            INT NOT NULL DEFAULT 1,
    status                 VARCHAR(32) NOT NULL DEFAULT 'accepted',
    failure_reason         VARCHAR(255),
    hold_id                UUID REFERENCES holds(id),
    sell_price             NUMERIC(18,6) NOT NULL DEFAULT 0,
    sell_currency          VARCHAR(3) NOT NULL DEFAULT 'USD',
    estimated_buy_cost     NUMERIC(18,6),
    actual_buy_cost        NUMERIC(18,6),
    buy_currency           VARCHAR(3),
    route_plan_id          UUID REFERENCES route_plans(id),
    provider_connection_id UUID REFERENCES provider_connections(id),
    provider_message_id    VARCHAR(128),
    attempts               INT NOT NULL DEFAULT 0,
    max_attempts           INT NOT NULL DEFAULT 3,
    expires_at             TIMESTAMPTZ NOT NULL,
    submitted_at           TIMESTAMPTZ,
    delivered_at           TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_messages PRIMARY KEY (id, created_at),
    CONSTRAINT chk_messages_status CHECK (status IN (
        'accepted', 'queued', 'submitting', 'submitted',
        'delivered', 'undelivered', 'expired', 'failed',
        'unknown', 'rejected'
    ))
) PARTITION BY RANGE (created_at);

-- Initial Monthly Partitions and DEFAULT partition
CREATE TABLE messages_2026_11 PARTITION OF messages
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');

CREATE TABLE messages_2026_12 PARTITION OF messages
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');

CREATE TABLE messages_2027_01 PARTITION OF messages
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');

CREATE TABLE messages_default PARTITION OF messages DEFAULT;

-- Partition indexes
CREATE INDEX idx_messages_cust_created ON messages (customer_org_id, created_at DESC);
CREATE INDEX idx_messages_status ON messages (status);

-- 3. Partitioned Message Parts Table
CREATE TABLE message_parts (
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    message_id          UUID NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    part_number         INT NOT NULL,
    total_parts         INT NOT NULL,
    udh                 BYTEA,
    encrypted_part_body BYTEA NOT NULL,
    key_id              VARCHAR(64) NOT NULL,
    provider_message_id VARCHAR(128),
    status              VARCHAR(32) NOT NULL DEFAULT 'accepted',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_message_parts PRIMARY KEY (id, created_at),
    CONSTRAINT fk_message_parts_parent FOREIGN KEY (message_id, created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE
) PARTITION BY RANGE (created_at);

CREATE TABLE message_parts_2026_11 PARTITION OF message_parts
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');

CREATE TABLE message_parts_2026_12 PARTITION OF message_parts
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');

CREATE TABLE message_parts_2027_01 PARTITION OF message_parts
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');

CREATE TABLE message_parts_default PARTITION OF message_parts DEFAULT;

CREATE INDEX idx_message_parts_parent ON message_parts (message_id, created_at);

-- 4. Unpartitioned Idempotency Lookup Table
CREATE TABLE message_idempotency_lookups (
    customer_org_id    UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    client_reference   VARCHAR(128) NOT NULL,
    message_id         UUID NOT NULL,
    message_created_at TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_message_idempotency_lookups PRIMARY KEY (customer_org_id, client_reference),
    CONSTRAINT fk_message_idempotency_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE
);

-- 5. Unpartitioned Provider Message ID Lookup Table
CREATE TABLE message_provider_lookups (
    connection_id       UUID NOT NULL REFERENCES provider_connections(id) ON DELETE CASCADE,
    provider_message_id VARCHAR(128) NOT NULL,
    message_id          UUID NOT NULL,
    message_created_at  TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_message_provider_lookups PRIMARY KEY (connection_id, provider_message_id),
    CONSTRAINT fk_message_provider_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE
);

-- 6. Transactional Outbox Table
CREATE TABLE message_outbox (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id         UUID NOT NULL,
    message_created_at TIMESTAMPTZ NOT NULL,
    customer_org_id    UUID NOT NULL REFERENCES organizations(id),
    payload            JSONB NOT NULL,
    status             VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts           INT NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_until       TIMESTAMPTZ,
    locked_by          VARCHAR(64),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_message_outbox_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE,
    CONSTRAINT chk_outbox_status CHECK (status IN ('pending', 'sending', 'published', 'failed'))
);

CREATE INDEX idx_message_outbox_fetch ON message_outbox (status, next_attempt_at)
    WHERE status IN ('pending', 'sending');
CREATE INDEX idx_message_outbox_customer ON message_outbox (customer_org_id);

-- 7. Message Dispatch Attempts History Table
CREATE TABLE message_attempts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id          UUID NOT NULL,
    message_created_at  TIMESTAMPTZ NOT NULL,
    attempt_number      INT NOT NULL,
    connection_id       UUID NOT NULL REFERENCES provider_connections(id),
    status              VARCHAR(32) NOT NULL,
    provider_message_id VARCHAR(128),
    error_code          VARCHAR(64),
    error_message       TEXT,
    buy_cost            NUMERIC(18,6),
    buy_currency        VARCHAR(3),
    latency_ms          INT,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,
    CONSTRAINT fk_message_attempts_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE,
    CONSTRAINT chk_message_attempts_status CHECK (status IN ('submitting', 'accepted', 'retryable_reject', 'fatal_reject', 'unknown'))
);

CREATE INDEX idx_message_attempts_msg ON message_attempts (message_id, attempt_number);

-- 8. Append-Only Message Events Table
CREATE TABLE message_events (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id         UUID NOT NULL,
    message_created_at TIMESTAMPTZ NOT NULL,
    event_type         VARCHAR(64) NOT NULL,
    from_status        VARCHAR(32),
    to_status          VARCHAR(32) NOT NULL,
    actor_type         VARCHAR(32) NOT NULL DEFAULT 'system',
    details            JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_message_events_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE
);

CREATE INDEX idx_message_events_target ON message_events (message_id, created_at);

-- 9. Append-Only Message DLR Receipts Table
CREATE TABLE message_dlrs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id          UUID,
    message_created_at  TIMESTAMPTZ,
    connection_id       UUID REFERENCES provider_connections(id),
    provider_message_id VARCHAR(128) NOT NULL,
    status              VARCHAR(32) NOT NULL,
    error_code          VARCHAR(64),
    raw_receipt         TEXT,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_message_dlrs_target FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE SET NULL
);

CREATE INDEX idx_message_dlrs_prov_msg ON message_dlrs (provider_message_id);

-- 10. Immutability Triggers for Append-Only Tables
CREATE OR REPLACE FUNCTION block_messaging_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'Messaging event and DLR tables are strictly append-only: UPDATE and DELETE are prohibited';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_message_events_immutable
BEFORE UPDATE OR DELETE ON message_events
FOR EACH ROW EXECUTE FUNCTION block_messaging_append_only_mutation();

CREATE TRIGGER trg_message_dlrs_immutable
BEFORE UPDATE OR DELETE ON message_dlrs
FOR EACH ROW EXECUTE FUNCTION block_messaging_append_only_mutation();

-- 11. Update change_requests Entity Constraint for System Settings
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable',
        'system_setting'
    ));

-- 12. Register Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
  ('message.send', 'messaging', 'message', 'send', 'Submit outbound SMS messages'),
  ('message.view', 'messaging', 'message', 'view', 'View message metadata and delivery status'),
  ('message.content.view', 'messaging', 'message', 'view_content', 'View decrypted message payload and body'),
  ('message.cancel', 'messaging', 'message', 'cancel', 'Cancel queued outbound messages'),
  ('message.export', 'messaging', 'message', 'export', 'Export message logs and reports'),
  ('message.platform.view', 'messaging', 'message', 'platform_view', 'Platform-only: view provider identity, buy cost, margin and routing candidates'),
  ('message.platform.manage', 'messaging', 'message', 'platform_manage', 'Platform-only: manage outbox, trigger re-routes and handle message reapers')
ON CONFLICT (code) DO NOTHING;

-- Assign permissions to platform roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Super Admin', 'Platform Admin')
  AND p.module = 'messaging'
ON CONFLICT DO NOTHING;

-- Assign customer permissions to non-platform customer/reseller admin roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Org Admin', 'Customer Admin', 'Reseller Admin')
  AND p.code IN ('message.send', 'message.view', 'message.content.view', 'message.export')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Customer User', 'Org Member')
  AND p.code IN ('message.send', 'message.view')
ON CONFLICT DO NOTHING;


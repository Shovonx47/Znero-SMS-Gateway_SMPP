-- Phase 8 Chunk 3: DLR Overrides, Settlement Exceptions, Reconciliation & SMPP Sessions
-- Migration: 000011_phase8_chunk3.up.sql

-- 1. Provider Connection DLR Mappings (Custom overrides per upstream SMSC)
CREATE TABLE IF NOT EXISTS provider_connection_dlr_mappings (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES provider_connections(id) ON DELETE CASCADE,
    upstream_stat VARCHAR(32) NOT NULL,
    upstream_err  VARCHAR(32),
    mapped_status VARCHAR(32) NOT NULL,
    description   TEXT,
    version       INT NOT NULL DEFAULT 1,
    created_by    UUID REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_mapped_status CHECK (mapped_status IN ('delivered', 'undelivered', 'expired', 'failed', 'unknown'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_dlr_mapping_null_err 
ON provider_connection_dlr_mappings (connection_id, upstream_stat) 
WHERE upstream_err IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_dlr_mapping_with_err 
ON provider_connection_dlr_mappings (connection_id, upstream_stat, upstream_err) 
WHERE upstream_err IS NOT NULL;

-- 2. DLR Mappings History Table (Versioned audit trail)
CREATE TABLE IF NOT EXISTS provider_connection_dlr_mappings_history (
    history_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    mapping_id    UUID NOT NULL REFERENCES provider_connection_dlr_mappings(id) ON DELETE CASCADE,
    connection_id UUID NOT NULL,
    upstream_stat VARCHAR(32) NOT NULL,
    upstream_err  VARCHAR(32),
    mapped_status VARCHAR(32) NOT NULL,
    description   TEXT,
    version       INT NOT NULL,
    changed_by    UUID REFERENCES users(id),
    change_type   VARCHAR(16) NOT NULL,
    changed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dlr_mappings_hist_mapping ON provider_connection_dlr_mappings_history (mapping_id, version);

-- 3. Unmatched DLRs Buffer (Retry buffer before flagging)
CREATE TABLE IF NOT EXISTS unmatched_dlrs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id       UUID REFERENCES provider_connections(id) ON DELETE SET NULL,
    provider_message_id VARCHAR(128) NOT NULL,
    status              VARCHAR(32) NOT NULL,
    error_code          VARCHAR(64),
    raw_receipt         TEXT,
    match_status        VARCHAR(32) NOT NULL DEFAULT 'unmatched',
    retry_count         INT NOT NULL DEFAULT 0,
    last_retry_at       TIMESTAMPTZ,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_unmatched_status CHECK (match_status IN ('unmatched', 'matched', 'flagged_unmatched'))
);

CREATE INDEX IF NOT EXISTS idx_unmatched_dlrs_status ON unmatched_dlrs (match_status, received_at);
CREATE INDEX IF NOT EXISTS idx_unmatched_dlrs_prov_id ON unmatched_dlrs (connection_id, provider_message_id);

-- 4. Settlement Exceptions Table
CREATE TABLE IF NOT EXISTS settlement_exceptions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id         UUID NOT NULL,
    message_created_at TIMESTAMPTZ NOT NULL,
    customer_org_id    UUID REFERENCES organizations(id),
    hold_id            UUID REFERENCES holds(id),
    exception_type     VARCHAR(64) NOT NULL,
    severity           VARCHAR(32) NOT NULL DEFAULT 'warning',
    details            JSONB NOT NULL DEFAULT '{}'::jsonb,
    resolved           BOOLEAN NOT NULL DEFAULT false,
    resolved_by        UUID REFERENCES users(id),
    resolved_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_settlement_exc_msg FOREIGN KEY (message_id, message_created_at)
        REFERENCES messages (id, created_at) ON DELETE CASCADE,
    CONSTRAINT chk_settlement_exc_sev CHECK (severity IN ('info', 'warning', 'critical'))
);

CREATE INDEX IF NOT EXISTS idx_settlement_exc_resolved ON settlement_exceptions (resolved, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_settlement_exc_org ON settlement_exceptions (customer_org_id);

-- 5. Messaging Reconciliation Runs Table
CREATE TABLE IF NOT EXISTS messaging_reconciliation_runs (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    messages_checked  INT NOT NULL DEFAULT 0,
    discrepancy_count INT NOT NULL DEFAULT 0,
    summary_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
    initiated_by      UUID REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_reconcile_runs_started ON messaging_reconciliation_runs (started_at DESC);

-- 6. Live SMPP Connection Sessions State Persistence Table
CREATE TABLE IF NOT EXISTS provider_connection_sessions (
    connection_id  UUID PRIMARY KEY REFERENCES provider_connections(id) ON DELETE CASCADE,
    bound          BOOLEAN NOT NULL DEFAULT false,
    in_flight      INT NOT NULL DEFAULT 0,
    current_tps    NUMERIC(10,2) NOT NULL DEFAULT 0,
    success_count  BIGINT NOT NULL DEFAULT 0,
    failure_count  BIGINT NOT NULL DEFAULT 0,
    last_error_code VARCHAR(64),
    reconnect_count BIGINT NOT NULL DEFAULT 0,
    last_active_at TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 7. Update change_requests Entity Constraint for DLR Mappings
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable',
        'system_setting', 'dlr_mapping'
    ));

-- 8. Additional Messaging Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
  ('message.dlr_mapping.manage', 'messaging', 'dlr_mapping', 'manage', 'Create, update and configure DLR status mappings'),
  ('message.queue.view', 'messaging', 'queue', 'view', 'View real-time messaging queue backlog and worker status'),
  ('message.reconcile', 'messaging', 'reconcile', 'run', 'Run messaging reconciliation and manage settlement exceptions')
ON CONFLICT (code) DO NOTHING;

-- Assign to platform roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Super Admin', 'Platform Admin')
  AND p.code IN ('message.dlr_mapping.manage', 'message.queue.view', 'message.reconcile')
ON CONFLICT DO NOTHING;

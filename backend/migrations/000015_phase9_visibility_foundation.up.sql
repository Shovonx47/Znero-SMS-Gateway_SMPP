-- Migration 000015: Phase 9 Visibility Foundation
-- Schema for Analytics Rollups, Service Heartbeats, Notifications, Alerts, Incidents, Settings Registry, Saved Filters, and Export Jobs

-- 1. Analytics Cursors Table (Durable checkpointing for rollup engine)
CREATE TABLE IF NOT EXISTS analytics_cursors (
    cursor_name      VARCHAR(64) PRIMARY KEY,
    last_event_id    UUID,
    last_event_time  TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. Customer 15-Minute Rollup Table (Partitioned by Month)
CREATE TABLE analytics_rollup_customer_15m (
    bucket_start            TIMESTAMPTZ NOT NULL,
    organization_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    customer_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    currency                VARCHAR(3) NOT NULL,
    submitted_count         BIGINT NOT NULL DEFAULT 0,
    delivered_count         BIGINT NOT NULL DEFAULT 0,
    failed_count            BIGINT NOT NULL DEFAULT 0,
    undelivered_count       BIGINT NOT NULL DEFAULT 0,
    expired_count           BIGINT NOT NULL DEFAULT 0,
    unknown_count           BIGINT NOT NULL DEFAULT 0,
    parts_count             BIGINT NOT NULL DEFAULT 0,
    revenue_captured        NUMERIC(18,6) NOT NULL DEFAULT 0,
    revenue_held            NUMERIC(18,6) NOT NULL DEFAULT 0,
    revenue_refunded        NUMERIC(18,6) NOT NULL DEFAULT 0,
    latency_p50_ms          INT,
    latency_p95_ms          INT,
    latency_p99_ms          INT,
    latency_samples_count   BIGINT NOT NULL DEFAULT 0,
    latency_sum_ms          BIGINT NOT NULL DEFAULT 0,
    latency_histogram       JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_analytics_rollup_customer_15m PRIMARY KEY (bucket_start, customer_id, currency)
) PARTITION BY RANGE (bucket_start);

CREATE TABLE analytics_rollup_customer_15m_2026_11 PARTITION OF analytics_rollup_customer_15m
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');
CREATE TABLE analytics_rollup_customer_15m_2026_12 PARTITION OF analytics_rollup_customer_15m
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');
CREATE TABLE analytics_rollup_customer_15m_2027_01 PARTITION OF analytics_rollup_customer_15m
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');
CREATE TABLE analytics_rollup_customer_15m_default PARTITION OF analytics_rollup_customer_15m DEFAULT;

CREATE INDEX idx_rollup_cust_org ON analytics_rollup_customer_15m (organization_id, bucket_start DESC);
CREATE INDEX idx_rollup_cust_id ON analytics_rollup_customer_15m (customer_id, bucket_start DESC);

-- 3. Provider 15-Minute Rollup Table (PLATFORM ONLY, Partitioned by Month)
CREATE TABLE analytics_rollup_provider_15m (
    bucket_start            TIMESTAMPTZ NOT NULL,
    provider_id             UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    connection_id           UUID NOT NULL REFERENCES provider_connections(id) ON DELETE CASCADE,
    currency                VARCHAR(3) NOT NULL,
    submitted_count         BIGINT NOT NULL DEFAULT 0,
    delivered_count         BIGINT NOT NULL DEFAULT 0,
    failed_count            BIGINT NOT NULL DEFAULT 0,
    undelivered_count       BIGINT NOT NULL DEFAULT 0,
    expired_count           BIGINT NOT NULL DEFAULT 0,
    cost_estimated          NUMERIC(18,6) NOT NULL DEFAULT 0,
    latency_p50_ms          INT,
    latency_p95_ms          INT,
    latency_samples_count   BIGINT NOT NULL DEFAULT 0,
    latency_sum_ms          BIGINT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_analytics_rollup_provider_15m PRIMARY KEY (bucket_start, connection_id, currency)
) PARTITION BY RANGE (bucket_start);

CREATE TABLE analytics_rollup_provider_15m_2026_11 PARTITION OF analytics_rollup_provider_15m
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');
CREATE TABLE analytics_rollup_provider_15m_2026_12 PARTITION OF analytics_rollup_provider_15m
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');
CREATE TABLE analytics_rollup_provider_15m_2027_01 PARTITION OF analytics_rollup_provider_15m
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');
CREATE TABLE analytics_rollup_provider_15m_default PARTITION OF analytics_rollup_provider_15m DEFAULT;

CREATE INDEX idx_rollup_prov_id ON analytics_rollup_provider_15m (provider_id, bucket_start DESC);
CREATE INDEX idx_rollup_conn_id ON analytics_rollup_provider_15m (connection_id, bucket_start DESC);

-- 4. Destination 15-Minute Rollup Table (Partitioned by Month)
CREATE TABLE analytics_rollup_destination_15m (
    bucket_start            TIMESTAMPTZ NOT NULL,
    country_iso             VARCHAR(2) NOT NULL,
    operator_id             UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'::uuid,
    currency                VARCHAR(3) NOT NULL DEFAULT 'USD',
    submitted_count         BIGINT NOT NULL DEFAULT 0,
    delivered_count         BIGINT NOT NULL DEFAULT 0,
    failed_count            BIGINT NOT NULL DEFAULT 0,
    undelivered_count       BIGINT NOT NULL DEFAULT 0,
    revenue_estimated       NUMERIC(18,6) NOT NULL DEFAULT 0,
    cost_estimated          NUMERIC(18,6) NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_analytics_rollup_destination_15m PRIMARY KEY (bucket_start, country_iso, operator_id, currency)
) PARTITION BY RANGE (bucket_start);

CREATE TABLE analytics_rollup_destination_15m_2026_11 PARTITION OF analytics_rollup_destination_15m
    FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');
CREATE TABLE analytics_rollup_destination_15m_2026_12 PARTITION OF analytics_rollup_destination_15m
    FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');
CREATE TABLE analytics_rollup_destination_15m_2027_01 PARTITION OF analytics_rollup_destination_15m
    FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');
CREATE TABLE analytics_rollup_destination_15m_default PARTITION OF analytics_rollup_destination_15m DEFAULT;

CREATE INDEX idx_rollup_dest_country ON analytics_rollup_destination_15m (country_iso, bucket_start DESC);

-- 5. Service Heartbeats Table
CREATE TABLE IF NOT EXISTS service_heartbeats (
    role            VARCHAR(64) NOT NULL,
    instance_id     VARCHAR(128) NOT NULL,
    hostname        VARCHAR(255) NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    metrics_json    JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT pk_service_heartbeats PRIMARY KEY (role, instance_id)
);
CREATE INDEX IF NOT EXISTS idx_service_heartbeats_seen ON service_heartbeats(last_seen_at DESC);

-- 6. Notifications and Notification Preferences
CREATE TABLE IF NOT EXISTS notifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organizations(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id) ON DELETE CASCADE,
    category        VARCHAR(64) NOT NULL,
    severity        VARCHAR(32) NOT NULL DEFAULT 'info',
    title           VARCHAR(255) NOT NULL,
    body            TEXT NOT NULL,
    link_url        VARCHAR(512),
    read_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_notifications_user_created ON notifications(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_org_created ON notifications(organization_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_preferences (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    email_enabled       BOOLEAN NOT NULL DEFAULT true,
    webhook_enabled     BOOLEAN NOT NULL DEFAULT false,
    webhook_url         VARCHAR(512),
    categories          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 7. Alert Rules, Alerts, and Incident Management
CREATE TABLE IF NOT EXISTS alert_rules (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(128) NOT NULL,
    condition_type      VARCHAR(64) NOT NULL,
    threshold           NUMERIC(18,4) NOT NULL,
    duration_seconds    INT NOT NULL DEFAULT 60,
    severity            VARCHAR(32) NOT NULL DEFAULT 'warning',
    scope               VARCHAR(32) NOT NULL DEFAULT 'platform',
    enabled             BOOLEAN NOT NULL DEFAULT true,
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alerts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id             UUID REFERENCES alert_rules(id) ON DELETE SET NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'firing',
    severity            VARCHAR(32) NOT NULL,
    summary             VARCHAR(255) NOT NULL,
    description         TEXT NOT NULL,
    labels              JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status);
CREATE INDEX IF NOT EXISTS idx_alerts_started ON alerts(started_at DESC);

CREATE TABLE IF NOT EXISTS incidents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title               VARCHAR(255) NOT NULL,
    severity            VARCHAR(32) NOT NULL DEFAULT 'major',
    status              VARCHAR(32) NOT NULL DEFAULT 'open',
    lead_user_id        UUID REFERENCES users(id) ON DELETE SET NULL,
    root_cause          TEXT,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);
CREATE INDEX IF NOT EXISTS idx_incidents_started ON incidents(started_at DESC);

CREATE TABLE IF NOT EXISTS incident_timeline (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id         UUID NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    author_user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    entry_type          VARCHAR(32) NOT NULL DEFAULT 'note',
    content             TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_incident_timeline_inc ON incident_timeline(incident_id, created_at ASC);

-- 8. Typed Settings Registry, User Preferences, and Saved Filters
CREATE TABLE IF NOT EXISTS settings_registry (
    key                 VARCHAR(128) PRIMARY KEY,
    value_json          JSONB NOT NULL,
    value_type          VARCHAR(32) NOT NULL,
    category            VARCHAR(64) NOT NULL,
    description         TEXT,
    is_secret           BOOLEAN NOT NULL DEFAULT false,
    updated_by_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed core platform settings
INSERT INTO settings_registry (key, value_json, value_type, category, description) VALUES
    ('system.company_name', '"Znero International"', 'string', 'general', 'Platform operating entity name'),
    ('system.support_email', '"support@znero.com"', 'string', 'general', 'Primary support and operations contact email'),
    ('system.default_timezone', '"UTC"', 'string', 'general', 'Default system timezone for reporting'),
    ('system.maintenance_mode', 'false', 'boolean', 'general', 'Global maintenance mode flag'),
    ('messaging.default_max_tps', '100', 'integer', 'messaging', 'Default throughput limit per customer connection'),
    ('messaging.default_dlr_wait_hours', '24', 'integer', 'messaging', 'Default wait window for DLR resolution in hours'),
    ('messaging.retention_days', '90', 'integer', 'messaging', 'Message and rollup retention period in days')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS saved_filters (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    view_name           VARCHAR(64) NOT NULL,
    filter_name         VARCHAR(128) NOT NULL,
    query_json          JSONB NOT NULL,
    is_default          BOOLEAN NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_saved_filters_user_view ON saved_filters(user_id, view_name);

CREATE TABLE IF NOT EXISTS user_preferences (
    user_id             UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    theme               VARCHAR(16) NOT NULL DEFAULT 'system',
    locale              VARCHAR(16) NOT NULL DEFAULT 'en',
    timezone            VARCHAR(64) NOT NULL DEFAULT 'UTC',
    dashboard_layout    JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 9. Export Jobs Table
CREATE TABLE IF NOT EXISTS export_jobs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    export_type         VARCHAR(64) NOT NULL,
    format              VARCHAR(16) NOT NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'pending',
    filter_params       JSONB NOT NULL DEFAULT '{}'::jsonb,
    row_count           BIGINT DEFAULT 0,
    file_path           VARCHAR(512),
    error_message       TEXT,
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_export_jobs_user ON export_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_export_jobs_org ON export_jobs(organization_id, created_at DESC);

-- 10. Register Permissions for Phase 9 Visibility
INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('analytics.dashboard.view', 'analytics', 'dashboard', 'view', 'View executive, NOC, reseller, and customer dashboards'),
    ('analytics.noc.view',       'analytics', 'noc',       'view', 'View platform NOC dashboard and carrier stats'),
    ('analytics.report.view',    'analytics', 'report',    'view', 'View scheduled and ad-hoc analytics reports'),
    ('analytics.export.create',  'analytics', 'export',    'create', 'Create data export jobs'),
    ('analytics.export.view',    'analytics', 'export',    'view', 'Download generated export files'),
    ('notification.view',        'notification', 'inbox',  'view', 'View in-app notifications and announcements'),
    ('notification.manage',      'notification', 'config', 'manage', 'Manage notification preferences and delivery channels'),
    ('alert.view',               'alert', 'rule',          'view', 'View system alert rules and firing alerts'),
    ('alert.manage',             'alert', 'rule',          'manage', 'Configure alert rules, thresholds, and silences'),
    ('incident.view',            'incident', 'incident',   'view', 'View platform incidents and incident timeline'),
    ('incident.manage',          'incident', 'incident',   'manage', 'Declare, update, and resolve platform incidents'),
    ('setting.view',             'setting', 'registry',    'view', 'View platform and tenant system settings'),
    ('setting.manage',           'setting', 'registry',    'manage', 'Modify system settings and operational parameters')
ON CONFLICT (code) DO NOTHING;

-- Grant permissions to Platform Super Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Super Admin', 'Platform Admin')
  AND p.code IN (
    'analytics.dashboard.view', 'analytics.noc.view', 'analytics.report.view',
    'analytics.export.create', 'analytics.export.view',
    'notification.view', 'notification.manage',
    'alert.view', 'alert.manage',
    'incident.view', 'incident.manage',
    'setting.view', 'setting.manage'
  )
ON CONFLICT DO NOTHING;

-- Grant tenant permissions to Org Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id
CROSS JOIN permissions p
WHERE r.name IN ('Org Admin', 'Customer Admin', 'Reseller Admin')
  AND p.code IN (
    'analytics.dashboard.view', 'analytics.report.view',
    'analytics.export.create', 'analytics.export.view',
    'notification.view', 'notification.manage',
    'setting.view'
  )
ON CONFLICT DO NOTHING;

-- Grant view permissions to Staff and Read-only
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Staff', 'Read-only', 'Org Member', 'Customer User')
  AND p.code IN (
    'analytics.dashboard.view', 'analytics.report.view',
    'analytics.export.view',
    'notification.view'
  )
ON CONFLICT DO NOTHING;

-- 11. Ensure ownership by znero_app if role exists
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'znero_app') THEN
        ALTER TABLE analytics_cursors OWNER TO znero_app;
        ALTER TABLE analytics_rollup_customer_15m OWNER TO znero_app;
        ALTER TABLE analytics_rollup_customer_15m_2026_11 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_customer_15m_2026_12 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_customer_15m_2027_01 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_customer_15m_default OWNER TO znero_app;
        ALTER TABLE analytics_rollup_provider_15m OWNER TO znero_app;
        ALTER TABLE analytics_rollup_provider_15m_2026_11 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_provider_15m_2026_12 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_provider_15m_2027_01 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_provider_15m_default OWNER TO znero_app;
        ALTER TABLE analytics_rollup_destination_15m OWNER TO znero_app;
        ALTER TABLE analytics_rollup_destination_15m_2026_11 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_destination_15m_2026_12 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_destination_15m_2027_01 OWNER TO znero_app;
        ALTER TABLE analytics_rollup_destination_15m_default OWNER TO znero_app;
        ALTER TABLE service_heartbeats OWNER TO znero_app;
        ALTER TABLE notifications OWNER TO znero_app;
        ALTER TABLE notification_preferences OWNER TO znero_app;
        ALTER TABLE alert_rules OWNER TO znero_app;
        ALTER TABLE alerts OWNER TO znero_app;
        ALTER TABLE incidents OWNER TO znero_app;
        ALTER TABLE incident_timeline OWNER TO znero_app;
        ALTER TABLE settings_registry OWNER TO znero_app;
        ALTER TABLE saved_filters OWNER TO znero_app;
        ALTER TABLE user_preferences OWNER TO znero_app;
        ALTER TABLE export_jobs OWNER TO znero_app;
    END IF;
END $$;

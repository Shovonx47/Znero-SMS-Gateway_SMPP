-- =============================================================================
-- PHASE 4: PROVIDERS, PROVIDER COVERAGE, CUSTOMER COMMERCIAL PROFILES,
--          CUSTOMER CONNECTION CREDENTIALS, AND SENDER ID REGISTRY
-- =============================================================================

-- 1. Code-Defined Phase 4 Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
    -- Providers (Platform-only)
    ('provider.provider.view',     'provider', 'provider',   'view',     'View upstream providers'),
    ('provider.provider.create',   'provider', 'provider',   'create',   'Create upstream providers'),
    ('provider.provider.edit',     'provider', 'provider',   'edit',     'Modify upstream provider details'),
    ('provider.provider.status',   'provider', 'provider',   'status',   'Activate, suspend, or deactivate upstream providers'),
    ('provider.provider.archive',  'provider', 'provider',   'archive',  'Archive upstream providers'),
    ('provider.provider.approve',  'provider', 'provider',   'approve',  'Review and approve/reject provider change requests'),
    ('provider.provider.override', 'provider', 'provider',   'override', 'Bypass maker-checker approval for provider changes'),

    -- Provider Connections (Platform-only, with separate secret.update permission)
    ('provider.connection.view',          'provider', 'connection', 'view',          'View upstream provider connection configurations'),
    ('provider.connection.create',        'provider', 'connection', 'create',        'Create upstream provider connections'),
    ('provider.connection.edit',          'provider', 'connection', 'edit',          'Modify upstream provider connection non-secret parameters'),
    ('provider.connection.secret.update', 'provider', 'connection', 'secret.update', 'Set or replace write-only provider connection credentials'),
    ('provider.connection.status',        'provider', 'connection', 'status',        'Activate or disable upstream provider connections'),
    ('provider.connection.archive',       'provider', 'connection', 'archive',       'Archive upstream provider connections'),
    ('provider.connection.approve',       'provider', 'connection', 'approve',       'Review and approve/reject provider connection change requests'),
    ('provider.connection.override',      'provider', 'connection', 'override',      'Bypass maker-checker approval for provider connection changes'),

    -- Provider Coverage (Platform-only)
    ('provider.coverage.view',     'provider', 'coverage', 'view',     'View provider country/operator coverage declarations'),
    ('provider.coverage.create',   'provider', 'coverage', 'create',   'Create provider coverage records'),
    ('provider.coverage.edit',     'provider', 'coverage', 'edit',     'Modify provider coverage records'),
    ('provider.coverage.status',   'provider', 'coverage', 'status',   'Change provider coverage status (declared/tested/active/suspended)'),
    ('provider.coverage.archive',  'provider', 'coverage', 'archive',  'Archive provider coverage records'),
    ('provider.coverage.import',   'provider', 'coverage', 'import',   'Bulk import provider coverage declarations'),
    ('provider.coverage.approve',  'provider', 'coverage', 'approve',  'Review and approve/reject provider coverage change requests'),
    ('provider.coverage.override', 'provider', 'coverage', 'override', 'Bypass maker-checker approval for provider coverage changes'),

    -- Customer Commercial Profiles (Hierarchy-scoped)
    ('customer.profile.view',     'customer', 'profile', 'view',     'View customer commercial profiles'),
    ('customer.profile.create',   'customer', 'profile', 'create',   'Create customer commercial profiles'),
    ('customer.profile.edit',     'customer', 'profile', 'edit',     'Edit customer commercial profiles'),
    ('customer.profile.approve',  'customer', 'profile', 'approve',  'Review and approve/reject customer profile change requests'),
    ('customer.profile.override', 'customer', 'profile', 'override', 'Bypass maker-checker approval for sensitive customer profile changes'),

    -- Customer Connection Credentials (Hierarchy-scoped)
    ('customer.apikey.view',          'customer', 'apikey',      'view',   'View customer API key metadata and prefixes'),
    ('customer.apikey.create',        'customer', 'apikey',      'create', 'Generate new customer API keys'),
    ('customer.apikey.revoke',        'customer', 'apikey',      'revoke', 'Revoke customer API keys'),
    ('customer.apikey.rotate',        'customer', 'apikey',      'rotate', 'Rotate customer API keys'),
    ('customer.smppaccount.view',     'customer', 'smppaccount', 'view',   'View customer SMPP account configurations'),
    ('customer.smppaccount.create',   'customer', 'smppaccount', 'create', 'Create customer SMPP accounts'),
    ('customer.smppaccount.edit',     'customer', 'smppaccount', 'edit',   'Modify customer SMPP account settings or reset password'),
    ('customer.smppaccount.revoke',   'customer', 'smppaccount', 'revoke', 'Disable or suspend customer SMPP accounts'),

    -- Sender ID Registry
    ('customer.senderid.view',     'customer', 'senderid', 'view',     'View registered and pending Sender IDs'),
    ('customer.senderid.create',   'customer', 'senderid', 'create',   'Submit Sender ID registration requests'),
    ('customer.senderid.edit',     'customer', 'senderid', 'edit',     'Edit or suspend Sender ID records'),
    ('customer.senderid.approve',  'customer', 'senderid', 'approve',  'Approve or reject pending Sender ID requests'),
    ('customer.senderid.override', 'customer', 'senderid', 'override', 'Bypass approval when creating or updating Sender IDs'),
    ('customer.senderid.import',   'customer', 'senderid', 'import',   'Bulk import Sender ID records')
ON CONFLICT (code) DO NOTHING;

-- Grant all Phase 4 permissions to Platform Super Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Super Admin'
  AND (p.module IN ('provider', 'customer'))
ON CONFLICT DO NOTHING;

-- Grant maker permissions (without override or approve) to Platform Master Data Editor
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Master Data Editor'
  AND p.code IN (
      'provider.provider.view', 'provider.provider.create', 'provider.provider.edit', 'provider.provider.status', 'provider.provider.archive',
      'provider.connection.view', 'provider.connection.create', 'provider.connection.edit', 'provider.connection.secret.update', 'provider.connection.status', 'provider.connection.archive',
      'provider.coverage.view', 'provider.coverage.create', 'provider.coverage.edit', 'provider.coverage.status', 'provider.coverage.archive', 'provider.coverage.import',
      'customer.profile.view', 'customer.profile.create', 'customer.profile.edit',
      'customer.apikey.view', 'customer.apikey.create', 'customer.apikey.revoke', 'customer.apikey.rotate',
      'customer.smppaccount.view', 'customer.smppaccount.create', 'customer.smppaccount.edit', 'customer.smppaccount.revoke',
      'customer.senderid.view', 'customer.senderid.create', 'customer.senderid.edit', 'customer.senderid.import'
  )
ON CONFLICT DO NOTHING;

-- Grant checker permissions to Platform Master Data Approver
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Master Data Approver'
  AND p.code IN (
      'provider.provider.view', 'provider.provider.approve',
      'provider.connection.view', 'provider.connection.approve',
      'provider.coverage.view', 'provider.coverage.approve',
      'customer.profile.view', 'customer.profile.approve',
      'customer.senderid.view', 'customer.senderid.approve'
  )
ON CONFLICT DO NOTHING;

-- Grant reseller Org Admin permissions (customer profile, credentials, sender ID requests — NEVER provider.*)
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'reseller'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'customer.profile.view', 'customer.profile.create', 'customer.profile.edit',
      'customer.apikey.view', 'customer.apikey.create', 'customer.apikey.revoke', 'customer.apikey.rotate',
      'customer.smppaccount.view', 'customer.smppaccount.create', 'customer.smppaccount.edit', 'customer.smppaccount.revoke',
      'customer.senderid.view', 'customer.senderid.create', 'customer.senderid.edit'
  )
ON CONFLICT DO NOTHING;

-- Grant customer Org Admin permissions (view own profile, manage own API keys, view SMPP accounts, request Sender IDs)
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'customer'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'customer.profile.view',
      'customer.apikey.view', 'customer.apikey.create', 'customer.apikey.revoke', 'customer.apikey.rotate',
      'customer.smppaccount.view',
      'customer.senderid.view', 'customer.senderid.create'
  )
ON CONFLICT DO NOTHING;

-- Grant read-only customer/reseller permissions to Read-only & Staff roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id
CROSS JOIN permissions p
WHERE r.name IN ('Read-only', 'Staff')
  AND p.code IN (
      'customer.profile.view',
      'customer.apikey.view',
      'customer.smppaccount.view',
      'customer.senderid.view'
  )
ON CONFLICT DO NOTHING;

-- 2. Extend change_requests and import_jobs CHECK constraints for Phase 4 entity types
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id'
    ));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check
    CHECK (operation IN (
        'create', 'update', 'deactivate', 'activate', 'archive', 'rollback',
        'confirm_import', 'status_change', 'secret_update', 'approve_sender_id'
    ));

ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_entity_type_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_entity_type_check
    CHECK (entity_type IN ('country', 'operator', 'prefix', 'provider_coverage', 'sender_id'));

-- 3. Providers (Upstream Suppliers — Platform-owned)
CREATE TABLE providers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(255) NOT NULL,
    code                VARCHAR(64) NOT NULL UNIQUE,
    provider_type       VARCHAR(32) NOT NULL DEFAULT 'smpp' CHECK (provider_type IN ('smpp', 'http', 'other')),
    legal_entity_name   VARCHAR(255) NOT NULL DEFAULT '',
    contact_technical   TEXT NOT NULL DEFAULT '',
    contact_commercial  TEXT NOT NULL DEFAULT '',
    contact_noc         TEXT NOT NULL DEFAULT '',
    country_id          UUID REFERENCES countries(id) ON DELETE RESTRICT,
    timezone            VARCHAR(64) NOT NULL DEFAULT 'UTC',
    currency_code       CHAR(3) NOT NULL DEFAULT 'USD',
    notes               TEXT NOT NULL DEFAULT '',
    status              VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'suspended', 'inactive')),
    archived            BOOLEAN NOT NULL DEFAULT false,
    version             INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by          UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_providers_status ON providers(status) WHERE NOT archived;
CREATE INDEX idx_providers_type ON providers(provider_type);
CREATE INDEX idx_providers_country ON providers(country_id);

CREATE TABLE providers_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id     UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_providers_history_version UNIQUE (provider_id, version)
);

CREATE INDEX idx_providers_history_provider_id ON providers_history(provider_id);

-- 4. Provider Connections (Configuration only; secrets encrypted at rest and excluded from snapshots)
CREATE TABLE provider_connections (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id                 UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    name                        VARCHAR(255) NOT NULL,
    protocol                    VARCHAR(16) NOT NULL CHECK (protocol IN ('smpp', 'http')),
    role_notes                  TEXT NOT NULL DEFAULT '',
    status                      VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'disabled')),
    archived                    BOOLEAN NOT NULL DEFAULT false,
    max_tps                     INT NOT NULL DEFAULT 10 CHECK (max_tps >= 1 AND max_tps <= 100000),

    -- SMPP protocol fields
    smpp_host                   VARCHAR(255) NOT NULL DEFAULT '',
    smpp_port                   INT NOT NULL DEFAULT 2775,
    smpp_system_id              VARCHAR(64) NOT NULL DEFAULT '',
    smpp_password_encrypted     TEXT NOT NULL DEFAULT '',
    smpp_password_changed_at    TIMESTAMPTZ,
    smpp_system_type            VARCHAR(32) NOT NULL DEFAULT '',
    smpp_bind_type              VARCHAR(32) NOT NULL DEFAULT 'transceiver' CHECK (smpp_bind_type IN ('transceiver', 'transmitter', 'receiver')),
    smpp_tls                    BOOLEAN NOT NULL DEFAULT true,
    smpp_window_size            INT NOT NULL DEFAULT 10,
    smpp_enquire_link_sec       INT NOT NULL DEFAULT 30,
    smpp_source_ton             INT NOT NULL DEFAULT 5,
    smpp_source_npi             INT NOT NULL DEFAULT 0,
    smpp_dest_ton               INT NOT NULL DEFAULT 1,
    smpp_dest_npi               INT NOT NULL DEFAULT 1,
    smpp_charset                VARCHAR(32) NOT NULL DEFAULT 'GSM7',
    ip_allowlist_notes          TEXT NOT NULL DEFAULT '',

    -- HTTP protocol fields
    http_base_url               TEXT NOT NULL DEFAULT '',
    http_auth_type              VARCHAR(32) NOT NULL DEFAULT 'none' CHECK (http_auth_type IN ('none', 'basic', 'header_token', 'query_token')),
    http_credentials_encrypted  TEXT NOT NULL DEFAULT '',
    http_credentials_changed_at TIMESTAMPTZ,
    http_format_notes           TEXT NOT NULL DEFAULT '',
    http_dlr_callback_notes     TEXT NOT NULL DEFAULT '',

    version                     INT NOT NULL DEFAULT 1,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by                  UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by                  UUID REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT uq_provider_connections_name UNIQUE (provider_id, name)
);

CREATE INDEX idx_provider_connections_provider_id ON provider_connections(provider_id);
CREATE INDEX idx_provider_connections_status ON provider_connections(status) WHERE NOT archived;

CREATE TABLE provider_connections_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id   UUID NOT NULL REFERENCES provider_connections(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_provider_connections_history_version UNIQUE (connection_id, version)
);

CREATE INDEX idx_provider_connections_history_conn_id ON provider_connections_history(connection_id);

-- 5. Provider Coverage (Provider -> Country -> Optional Operator; separate from master data)
CREATE TABLE provider_coverage (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id     UUID NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    country_id      UUID NOT NULL REFERENCES countries(id) ON DELETE RESTRICT,
    operator_id     UUID REFERENCES operators(id) ON DELETE RESTRICT,
    status          VARCHAR(32) NOT NULL DEFAULT 'declared' CHECK (status IN ('declared', 'tested', 'active', 'suspended')),
    notes           TEXT NOT NULL DEFAULT '',
    effective_from  TIMESTAMPTZ,
    effective_to    TIMESTAMPTZ,
    data_source     TEXT NOT NULL DEFAULT '',
    archived        BOOLEAN NOT NULL DEFAULT false,
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX uq_provider_coverage_target
    ON provider_coverage (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX idx_provider_coverage_provider ON provider_coverage(provider_id);
CREATE INDEX idx_provider_coverage_country ON provider_coverage(country_id);
CREATE INDEX idx_provider_coverage_operator ON provider_coverage(operator_id);

CREATE TABLE provider_coverage_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    coverage_id     UUID NOT NULL REFERENCES provider_coverage(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_provider_coverage_history_version UNIQUE (coverage_id, version)
);

CREATE INDEX idx_provider_coverage_history_cov_id ON provider_coverage_history(coverage_id);

-- 6. Customer Commercial Profiles (1-to-1 with organizations of type 'customer')
CREATE TABLE customer_profiles (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL UNIQUE REFERENCES organizations(id) ON DELETE RESTRICT,
    account_type        VARCHAR(32) NOT NULL DEFAULT 'prepaid' CHECK (account_type IN ('prepaid', 'postpaid')),
    billing_currency    CHAR(3) NOT NULL DEFAULT 'USD',
    timezone            VARCHAR(64) NOT NULL DEFAULT 'UTC',
    default_language    VARCHAR(16) NOT NULL DEFAULT 'en',
    technical_contact   TEXT NOT NULL DEFAULT '',
    billing_contact     TEXT NOT NULL DEFAULT '',
    credit_limit        NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (credit_limit >= 0),
    default_max_tps     INT NOT NULL DEFAULT 10 CHECK (default_max_tps >= 1 AND default_max_tps <= 100000),
    daily_message_cap   BIGINT NOT NULL DEFAULT 10000 CHECK (daily_message_cap >= 0),
    monthly_message_cap BIGINT NOT NULL DEFAULT 300000 CHECK (monthly_message_cap >= 0),
    allowed_countries   TEXT[] NOT NULL DEFAULT '{}',
    destination_policy  VARCHAR(32) NOT NULL DEFAULT 'allow_all' CHECK (destination_policy IN ('allow_all', 'allow_listed', 'block_listed')),
    status              VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'suspended', 'closed')),
    notes               TEXT NOT NULL DEFAULT '',
    archived            BOOLEAN NOT NULL DEFAULT false,
    version             INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by          UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_customer_profiles_org ON customer_profiles(organization_id);
CREATE INDEX idx_customer_profiles_status ON customer_profiles(status);

CREATE TABLE customer_profiles_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id      UUID NOT NULL REFERENCES customer_profiles(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_customer_profiles_history_version UNIQUE (profile_id, version)
);

CREATE INDEX idx_customer_profiles_history_prof_id ON customer_profiles_history(profile_id);

-- 7. Customer Connection Credentials (API Keys & SMPP Accounts)
CREATE TABLE customer_api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    label           VARCHAR(128) NOT NULL,
    key_prefix      VARCHAR(32) NOT NULL,
    key_hash        VARCHAR(64) NOT NULL UNIQUE,
    scopes          TEXT[] NOT NULL DEFAULT '{}',
    ip_allowlist    TEXT[] NOT NULL DEFAULT '{}',
    status          VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX idx_customer_api_keys_org ON customer_api_keys(organization_id);
CREATE INDEX idx_customer_api_keys_status ON customer_api_keys(status);

CREATE TABLE customer_smpp_accounts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    system_id           VARCHAR(64) NOT NULL UNIQUE,
    password_hash       TEXT NOT NULL,
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    allowed_ips         TEXT[] NOT NULL DEFAULT '{}',
    allowed_bind_types  TEXT[] NOT NULL DEFAULT '{transceiver}',
    max_binds           INT NOT NULL DEFAULT 2 CHECK (max_binds >= 1 AND max_binds <= 100),
    max_tps             INT NOT NULL DEFAULT 10 CHECK (max_tps >= 1 AND max_tps <= 100000),
    status              VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'suspended')),
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_customer_smpp_accounts_org ON customer_smpp_accounts(organization_id);

-- 8. Sender ID Registry
CREATE TABLE sender_ids (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    sender_id       VARCHAR(32) NOT NULL,
    sender_type     VARCHAR(32) NOT NULL CHECK (sender_type IN ('alphanumeric', 'numeric', 'shortcode')),
    country_id      UUID REFERENCES countries(id) ON DELETE RESTRICT,
    status          VARCHAR(32) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'suspended')),
    approval_notes  TEXT NOT NULL DEFAULT '',
    effective_from  TIMESTAMPTZ,
    effective_to    TIMESTAMPTZ,
    data_source     TEXT NOT NULL DEFAULT '',
    archived        BOOLEAN NOT NULL DEFAULT false,
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX uq_sender_ids_org_val_country
    ON sender_ids (organization_id, UPPER(sender_id), COALESCE(country_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX idx_sender_ids_org ON sender_ids(organization_id);
CREATE INDEX idx_sender_ids_status ON sender_ids(status);
CREATE INDEX idx_sender_ids_country ON sender_ids(country_id);

CREATE TABLE sender_ids_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_id_ref   UUID NOT NULL REFERENCES sender_ids(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_sender_ids_history_version UNIQUE (sender_id_ref, version)
);

CREATE INDEX idx_sender_ids_history_ref ON sender_ids_history(sender_id_ref);

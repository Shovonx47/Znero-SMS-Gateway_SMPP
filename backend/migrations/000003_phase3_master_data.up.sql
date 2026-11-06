-- Step 1: Code-Defined Master Data Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('country.country.view', 'country', 'country', 'view', 'View country reference data'),
    ('country.country.create', 'country', 'country', 'create', 'Create country records'),
    ('country.country.edit', 'country', 'country', 'edit', 'Modify country records'),
    ('country.country.deactivate', 'country', 'country', 'deactivate', 'Deactivate/activate country records'),
    ('country.country.archive', 'country', 'country', 'archive', 'Archive country records'),
    ('country.country.approve', 'country', 'country', 'approve', 'Review and approve/reject country change requests'),
    ('country.country.override', 'country', 'country', 'override', 'Bypass approval workflow for country changes'),
    ('country.country.import', 'country', 'country', 'import', 'Upload and import country reference data'),
    ('operator.operator.view', 'operator', 'operator', 'view', 'View operator reference data'),
    ('operator.operator.create', 'operator', 'operator', 'create', 'Create operator records'),
    ('operator.operator.edit', 'operator', 'operator', 'edit', 'Modify operator records'),
    ('operator.operator.deactivate', 'operator', 'operator', 'deactivate', 'Deactivate/activate operator records'),
    ('operator.operator.archive', 'operator', 'operator', 'archive', 'Archive operator records'),
    ('operator.operator.approve', 'operator', 'operator', 'approve', 'Review and approve/reject operator change requests'),
    ('operator.operator.override', 'operator', 'operator', 'override', 'Bypass approval workflow for operator changes'),
    ('operator.operator.import', 'operator', 'operator', 'import', 'Upload and import operator reference data'),
    ('number.prefix.view', 'number', 'prefix', 'view', 'View number prefix records'),
    ('number.prefix.create', 'number', 'prefix', 'create', 'Create number prefix records'),
    ('number.prefix.edit', 'number', 'prefix', 'edit', 'Modify number prefix records'),
    ('number.prefix.deactivate', 'number', 'prefix', 'deactivate', 'Deactivate/activate number prefix records'),
    ('number.prefix.archive', 'number', 'prefix', 'archive', 'Archive number prefix records'),
    ('number.prefix.approve', 'number', 'prefix', 'approve', 'Review and approve/reject prefix change requests'),
    ('number.prefix.override', 'number', 'prefix', 'override', 'Bypass approval workflow for prefix changes'),
    ('number.prefix.import', 'number', 'prefix', 'import', 'Upload and import number prefix reference data'),
    ('import.job.view', 'import', 'job', 'view', 'View import jobs and preview rows'),
    ('import.job.manage', 'import', 'job', 'manage', 'Upload files, confirm imports, and trigger rollbacks'),
    ('number.lookup.use', 'number', 'lookup', 'use', 'Perform single and bulk phone number lookups'),
    ('number.rule.view', 'number', 'rule', 'view', 'View number transformation rules'),
    ('number.rule.create', 'number', 'rule', 'create', 'Create number transformation rules'),
    ('number.rule.edit', 'number', 'rule', 'edit', 'Modify number transformation rules'),
    ('number.rule.delete', 'number', 'rule', 'delete', 'Deactivate or delete number transformation rules'),
    ('number.rule.approve', 'number', 'rule', 'approve', 'Review and approve number transformation rule changes');

-- Auto-grant all new permissions to platform Super Admin roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'Super Admin'
  AND p.code IN (
    'country.country.view', 'country.country.create', 'country.country.edit',
    'country.country.deactivate', 'country.country.archive', 'country.country.approve',
    'country.country.override', 'country.country.import',
    'operator.operator.view', 'operator.operator.create', 'operator.operator.edit',
    'operator.operator.deactivate', 'operator.operator.archive', 'operator.operator.approve',
    'operator.operator.override', 'operator.operator.import',
    'number.prefix.view', 'number.prefix.create', 'number.prefix.edit',
    'number.prefix.deactivate', 'number.prefix.archive', 'number.prefix.approve',
    'number.prefix.override', 'number.prefix.import',
    'import.job.view', 'import.job.manage',
    'number.lookup.use',
    'number.rule.view', 'number.rule.create', 'number.rule.edit', 'number.rule.delete', 'number.rule.approve'
  )
ON CONFLICT DO NOTHING;

-- Grant read-only permissions to Org Admin & Read-only roles in platform, reseller, and customer orgs
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Org Admin', 'Read-only')
  AND p.code IN (
    'country.country.view',
    'operator.operator.view',
    'number.prefix.view',
    'number.lookup.use',
    'number.rule.view'
  )
ON CONFLICT DO NOTHING;

-- Step 2: Countries Table
CREATE TABLE countries (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                        VARCHAR(255) NOT NULL,
    iso2                        CHAR(2) NOT NULL UNIQUE,
    iso3                        CHAR(3) NOT NULL UNIQUE,
    numeric_code                CHAR(3) NOT NULL UNIQUE,
    calling_code                VARCHAR(10) NOT NULL,
    continent                   VARCHAR(64) NOT NULL DEFAULT '',
    region                      VARCHAR(128) NOT NULL DEFAULT '',
    currency_code               CHAR(3) NOT NULL DEFAULT '',
    timezones                   TEXT[] NOT NULL DEFAULT '{}',
    numbering_plan_notes        TEXT NOT NULL DEFAULT '',
    min_national_length         INT NOT NULL DEFAULT 4,
    max_national_length         INT NOT NULL DEFAULT 15,
    sender_id_policy_notes      TEXT NOT NULL DEFAULT '',
    dnd_policy_notes            TEXT NOT NULL DEFAULT '',
    consent_requirement_notes   TEXT NOT NULL DEFAULT '',
    data_source                 TEXT NOT NULL DEFAULT '',
    verification_date           DATE,
    verification_status         VARCHAR(32) NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified', 'verified')),
    status                      VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'archived')),
    version                     INT NOT NULL DEFAULT 1,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by                  UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by                  UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_countries_iso2 ON countries(iso2);
CREATE INDEX idx_countries_iso3 ON countries(iso3);
CREATE INDEX idx_countries_calling_code ON countries(calling_code);
CREATE INDEX idx_countries_status ON countries(status);
CREATE INDEX idx_countries_verification_status ON countries(verification_status);

-- Countries Version History (Append-Only Snapshot Table)
CREATE TABLE countries_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id      UUID NOT NULL REFERENCES countries(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_countries_history_version UNIQUE (country_id, version)
);

CREATE INDEX idx_countries_history_country_id ON countries_history(country_id);

-- Step 3: Operators Table
CREATE TABLE operators (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(255) NOT NULL,
    brand_name          VARCHAR(255) NOT NULL DEFAULT '',
    parent_company      VARCHAR(255) NOT NULL DEFAULT '',
    country_id          UUID NOT NULL REFERENCES countries(id) ON DELETE RESTRICT,
    mcc                 CHAR(3) NOT NULL,
    mnc                 VARCHAR(3) NOT NULL,
    network_technology  TEXT[] NOT NULL DEFAULT '{}',
    capabilities        JSONB NOT NULL DEFAULT '{}',
    data_source         TEXT NOT NULL DEFAULT '',
    verification_status VARCHAR(32) NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified', 'verified')),
    effective_date      DATE,
    status              VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'archived')),
    version             INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT uq_operators_country_mcc_mnc UNIQUE (country_id, mcc, mnc)
);

CREATE INDEX idx_operators_country_id ON operators(country_id);
CREATE INDEX idx_operators_mcc ON operators(mcc);
CREATE INDEX idx_operators_status ON operators(status);
CREATE INDEX idx_operators_verification_status ON operators(verification_status);

-- Operators Version History
CREATE TABLE operators_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id     UUID NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_operators_history_version UNIQUE (operator_id, version)
);

CREATE INDEX idx_operators_history_operator_id ON operators_history(operator_id);

-- Step 4: Number Prefixes Table
CREATE TABLE number_prefixes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prefix          VARCHAR(20) NOT NULL,
    country_id      UUID NOT NULL REFERENCES countries(id) ON DELETE RESTRICT,
    operator_id     UUID REFERENCES operators(id) ON DELETE SET NULL,
    number_type     VARCHAR(32) NOT NULL DEFAULT 'mobile' CHECK (number_type IN ('mobile', 'fixed', 'other')),
    priority        INT NOT NULL DEFAULT 100,
    effective_from  TIMESTAMPTZ,
    effective_to    TIMESTAMPTZ,
    data_source     TEXT NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'archived')),
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_number_prefixes_prefix ON number_prefixes(prefix);
CREATE INDEX idx_number_prefixes_country_id ON number_prefixes(country_id);
CREATE INDEX idx_number_prefixes_operator_id ON number_prefixes(operator_id);
CREATE INDEX idx_number_prefixes_status ON number_prefixes(status);

-- Number Prefixes Version History
CREATE TABLE number_prefixes_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prefix_id       UUID NOT NULL REFERENCES number_prefixes(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_number_prefixes_history_version UNIQUE (prefix_id, version)
);

CREATE INDEX idx_number_prefixes_history_prefix_id ON number_prefixes_history(prefix_id);

-- Step 5: Change Requests (Maker-Checker Approval Engine)
CREATE TABLE change_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type     VARCHAR(32) NOT NULL CHECK (entity_type IN ('country', 'operator', 'prefix', 'number_rule', 'import_job')),
    entity_id       UUID,
    operation       VARCHAR(32) NOT NULL CHECK (operation IN ('create', 'update', 'deactivate', 'activate', 'archive', 'rollback', 'confirm_import')),
    proposed_data   JSONB NOT NULL,
    current_data    JSONB,
    status          VARCHAR(32) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    maker_user_id   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    made_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    checker_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    checked_at      TIMESTAMPTZ,
    reject_reason   TEXT NOT NULL DEFAULT '',
    entity_version  INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_change_requests_entity ON change_requests(entity_type, entity_id);
CREATE INDEX idx_change_requests_status ON change_requests(status);
CREATE INDEX idx_change_requests_maker ON change_requests(maker_user_id);

-- Step 6: Import Jobs & Rows Table
CREATE TABLE import_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type     VARCHAR(32) NOT NULL CHECK (entity_type IN ('country', 'operator', 'prefix')),
    file_name       VARCHAR(512) NOT NULL,
    file_size       BIGINT NOT NULL DEFAULT 0,
    content_type    VARCHAR(128) NOT NULL DEFAULT '',
    stored_path     TEXT NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'parsing', 'preview_ready', 'pending_approval', 'confirming', 'completed', 'failed', 'rolled_back')),
    total_rows      INT NOT NULL DEFAULT 0,
    valid_rows      INT NOT NULL DEFAULT 0,
    invalid_rows    INT NOT NULL DEFAULT 0,
    imported_rows   INT NOT NULL DEFAULT 0,
    skipped_rows    INT NOT NULL DEFAULT 0,
    allow_partial   BOOLEAN NOT NULL DEFAULT false,
    error_summary   JSONB NOT NULL DEFAULT '{}',
    actor_user_id   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_import_jobs_entity_type ON import_jobs(entity_type);
CREATE INDEX idx_import_jobs_status ON import_jobs(status);
CREATE INDEX idx_import_jobs_actor ON import_jobs(actor_user_id);

CREATE TABLE import_job_rows (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id              UUID NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    row_number          INT NOT NULL,
    raw_data            JSONB NOT NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'valid', 'invalid', 'duplicate', 'conflict', 'imported', 'skipped')),
    errors              JSONB NOT NULL DEFAULT '[]',
    created_entity_id   UUID
);

CREATE INDEX idx_import_job_rows_job_id ON import_job_rows(job_id);
CREATE INDEX idx_import_job_rows_status ON import_job_rows(job_id, status);

-- Step 7: Number Transformation Rules Table
CREATE TABLE number_transform_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    operation       VARCHAR(32) NOT NULL CHECK (operation IN ('add_prefix', 'remove_prefix', 'replace_prefix', 'add_country_code', 'remove_country_code')),
    match_pattern   VARCHAR(50) NOT NULL,
    params          JSONB NOT NULL DEFAULT '{}',
    scope           VARCHAR(32) NOT NULL DEFAULT 'global' CHECK (scope IN ('global', 'country', 'operator')),
    country_id      UUID REFERENCES countries(id) ON DELETE SET NULL,
    operator_id     UUID REFERENCES operators(id) ON DELETE SET NULL,
    direction       VARCHAR(32) NOT NULL DEFAULT 'both' CHECK (direction IN ('inbound', 'outbound', 'both')),
    priority        INT NOT NULL DEFAULT 100,
    active_from     TIMESTAMPTZ,
    active_to       TIMESTAMPTZ,
    status          VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'archived')),
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_number_transform_rules_status ON number_transform_rules(status);
CREATE INDEX idx_number_transform_rules_priority ON number_transform_rules(priority);
CREATE INDEX idx_number_transform_rules_scope ON number_transform_rules(scope);

-- Number Transformation Rules History
CREATE TABLE number_transform_rules_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id         UUID NOT NULL REFERENCES number_transform_rules(id) ON DELETE CASCADE,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason          TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_number_transform_rules_history_version UNIQUE (rule_id, version)
);

CREATE INDEX idx_number_transform_rules_history_rule_id ON number_transform_rules_history(rule_id);

-- =============================================================================
-- PHASE 5: RATING ENGINE, RATE CARDS, RATES, ASSIGNMENTS, AND MARGIN PROTECTION
-- =============================================================================

-- Enable btree_gist extension for GiST multi-column exclusion constraints
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- 1. Code-Defined Phase 5 Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
    -- Provider Buy Cards (Platform-only)
    ('rate.buycard.create',   'rate', 'buycard',    'create',   'Create provider buy rate cards'),
    ('rate.buycard.read',     'rate', 'buycard',    'read',     'View provider buy rate cards'),
    ('rate.buycard.update',   'rate', 'buycard',    'update',   'Modify provider buy rate cards'),
    ('rate.buycard.archive',  'rate', 'buycard',    'archive',  'Archive provider buy rate cards'),
    ('rate.buycard.approve',  'rate', 'buycard',    'approve',  'Review and approve/reject provider buy rate card change requests'),
    ('rate.buycard.override', 'rate', 'buycard',    'override', 'Bypass approval for provider buy rate card changes'),

    -- Provider Buy Rates (Platform-only)
    ('rate.buyrate.create',   'rate', 'buyrate',    'create',   'Add rates to provider buy rate cards'),
    ('rate.buyrate.read',     'rate', 'buyrate',    'read',     'View provider buy rates'),
    ('rate.buyrate.update',   'rate', 'buyrate',    'update',   'Modify or close provider buy rates'),
    ('rate.buyrate.approve',  'rate', 'buyrate',    'approve',  'Review and approve/reject provider buy rate change requests'),
    ('rate.buyrate.override', 'rate', 'buyrate',    'override', 'Bypass approval for provider buy rate changes'),

    -- Customer Sell Cards (Platform & Reseller scoped)
    ('rate.sellcard.create',  'rate', 'sellcard',   'create',   'Create customer sell rate cards'),
    ('rate.sellcard.read',    'rate', 'sellcard',   'read',     'View customer sell rate cards'),
    ('rate.sellcard.update',  'rate', 'sellcard',   'update',   'Modify customer sell rate cards'),
    ('rate.sellcard.archive', 'rate', 'sellcard',   'archive',  'Archive customer sell rate cards'),
    ('rate.sellcard.approve', 'rate', 'sellcard',   'approve',  'Review and approve/reject customer sell rate card change requests'),
    ('rate.sellcard.override','rate', 'sellcard',   'override', 'Bypass approval for customer sell rate card changes'),

    -- Customer Sell Rates (Platform, Reseller, and Customer read)
    ('rate.sellrate.create',  'rate', 'sellrate',   'create',   'Add rates to customer sell rate cards'),
    ('rate.sellrate.read',    'rate', 'sellrate',   'read',     'View customer sell rates'),
    ('rate.sellrate.update',  'rate', 'sellrate',   'update',   'Modify or close customer sell rates'),
    ('rate.sellrate.approve', 'rate', 'sellrate',   'approve',  'Review and approve/reject customer sell rate change requests'),
    ('rate.sellrate.override','rate', 'sellrate',   'override', 'Bypass approval for customer sell rate changes'),

    -- Rate Card Assignments
    ('rate.assignment.create',  'rate', 'assignment', 'create',   'Assign rate cards to customers or resellers'),
    ('rate.assignment.read',    'rate', 'assignment', 'read',     'View rate card assignments'),
    ('rate.assignment.update',  'rate', 'assignment', 'update',   'Modify or deactivate rate card assignments'),
    ('rate.assignment.approve', 'rate', 'assignment', 'approve',  'Review and approve/reject rate assignment change requests'),
    ('rate.assignment.override','rate', 'assignment', 'override', 'Bypass approval for rate assignment changes'),

    -- Margin Protection & Overrides (Platform-only)
    ('rate.margin.view',     'rate', 'margin',     'view',     'View margin analysis and reports'),
    ('rate.margin.override', 'rate', 'margin',     'override', 'Override margin violation when saving rates below cost'),

    -- Bulk Import
    ('rate.import.upload',   'rate', 'import',     'upload',   'Upload rate sheets for preview and validation'),
    ('rate.import.confirm',  'rate', 'import',     'confirm',  'Confirm and apply rate sheet imports')
ON CONFLICT (code) DO NOTHING;

-- Grant all Phase 5 permissions to Platform Super Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Super Admin'
  AND p.module = 'rate'
ON CONFLICT DO NOTHING;

-- Grant maker permissions (without override or approve) to Platform Master Data Editor
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Master Data Editor'
  AND p.code IN (
      'rate.buycard.create', 'rate.buycard.read', 'rate.buycard.update', 'rate.buycard.archive',
      'rate.buyrate.create', 'rate.buyrate.read', 'rate.buyrate.update',
      'rate.sellcard.create', 'rate.sellcard.read', 'rate.sellcard.update', 'rate.sellcard.archive',
      'rate.sellrate.create', 'rate.sellrate.read', 'rate.sellrate.update',
      'rate.assignment.create', 'rate.assignment.read', 'rate.assignment.update',
      'rate.margin.view',
      'rate.import.upload', 'rate.import.confirm'
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
      'rate.buycard.read', 'rate.buycard.approve',
      'rate.buyrate.read', 'rate.buyrate.approve',
      'rate.sellcard.read', 'rate.sellcard.approve',
      'rate.sellrate.read', 'rate.sellrate.approve',
      'rate.assignment.read', 'rate.assignment.approve',
      'rate.margin.view'
  )
ON CONFLICT DO NOTHING;

-- Grant sell card and rate management to Reseller Org Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'reseller'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'rate.sellcard.create', 'rate.sellcard.read', 'rate.sellcard.update', 'rate.sellcard.archive',
      'rate.sellrate.create', 'rate.sellrate.read', 'rate.sellrate.update',
      'rate.assignment.read',
      'rate.import.upload', 'rate.import.confirm'
  )
ON CONFLICT DO NOTHING;

-- Grant read-only assigned rates to Customer Org Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'customer'
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN (
      'rate.sellcard.read',
      'rate.sellrate.read'
  )
ON CONFLICT DO NOTHING;

-- 2. Rate Cards Table
CREATE TABLE rate_cards (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    kind            VARCHAR(32) NOT NULL CHECK (kind IN ('provider_buy', 'customer_sell')),
    owner_org_id    UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    provider_id     UUID REFERENCES providers(id) ON DELETE CASCADE,
    currency_code   VARCHAR(3) NOT NULL,
    channel         VARCHAR(32) NOT NULL DEFAULT 'sms',
    status          VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'inactive', 'archived')),
    description     TEXT NOT NULL DEFAULT '',
    archived        BOOLEAN NOT NULL DEFAULT false,
    version         INT NOT NULL DEFAULT 1,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_buy_card_provider CHECK (
        (kind = 'provider_buy' AND provider_id IS NOT NULL) OR
        (kind = 'customer_sell')
    )
);

CREATE INDEX idx_rate_cards_owner_org ON rate_cards(owner_org_id);
CREATE INDEX idx_rate_cards_kind ON rate_cards(kind);
CREATE INDEX idx_rate_cards_status ON rate_cards(status);
CREATE INDEX idx_rate_cards_provider ON rate_cards(provider_id) WHERE provider_id IS NOT NULL;

-- 3. Rate Cards History Table
CREATE TABLE rate_cards_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id         UUID NOT NULL REFERENCES rate_cards(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    kind            VARCHAR(32) NOT NULL,
    owner_org_id    UUID NOT NULL,
    provider_id     UUID,
    currency_code   VARCHAR(3) NOT NULL,
    channel         VARCHAR(32) NOT NULL,
    status          VARCHAR(32) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    archived        BOOLEAN NOT NULL DEFAULT false,
    version         INT NOT NULL,
    snapshot        JSONB NOT NULL,
    changed_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason   TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_rate_cards_history_version UNIQUE (card_id, version)
);

CREATE INDEX idx_rate_cards_history_card_id ON rate_cards_history(card_id);

-- 4. Rates Table
CREATE TABLE rates (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id             UUID NOT NULL REFERENCES rate_cards(id) ON DELETE CASCADE,
    country_id          UUID NOT NULL REFERENCES countries(id) ON DELETE RESTRICT,
    operator_id         UUID REFERENCES operators(id) ON DELETE RESTRICT,
    message_type        VARCHAR(32) NOT NULL DEFAULT 'default' CHECK (message_type IN ('default', 'transactional', 'promotional', 'otp')),
    price               NUMERIC(18,6) NOT NULL CHECK (price >= 0),
    min_billing_note    TEXT NOT NULL DEFAULT '',
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    source              TEXT NOT NULL DEFAULT 'manual',
    status              VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'superseded')),
    version             INT NOT NULL DEFAULT 1,
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_effective_to_after_from CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT uq_rate_effective_overlap EXCLUDE USING gist (
        card_id WITH =,
        country_id WITH =,
        (COALESCE(operator_id::text, '')) WITH =,
        message_type WITH =,
        tstzrange(effective_from, effective_to, '[)') WITH &&
    ) WHERE (status = 'active')
);

CREATE INDEX idx_rates_card_id ON rates(card_id);
CREATE INDEX idx_rates_lookup ON rates(card_id, country_id, operator_id, message_type, status);
CREATE INDEX idx_rates_effective ON rates(card_id, effective_from, effective_to);

-- 5. Rates History Table
CREATE TABLE rates_history (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rate_id             UUID NOT NULL REFERENCES rates(id) ON DELETE CASCADE,
    card_id             UUID NOT NULL,
    country_id          UUID NOT NULL,
    operator_id         UUID,
    message_type        VARCHAR(32) NOT NULL,
    price               NUMERIC(18,6) NOT NULL,
    min_billing_note    TEXT NOT NULL DEFAULT '',
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    source              TEXT NOT NULL DEFAULT 'manual',
    status              VARCHAR(32) NOT NULL,
    version             INT NOT NULL,
    snapshot            JSONB NOT NULL,
    changed_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason       TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_rates_history_version UNIQUE (rate_id, version)
);

CREATE INDEX idx_rates_history_rate_id ON rates_history(rate_id);

-- 6. Customer Rate Assignments Table
CREATE TABLE customer_rate_assignments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    card_id             UUID NOT NULL REFERENCES rate_cards(id) ON DELETE RESTRICT,
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    status              VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'superseded')),
    version             INT NOT NULL DEFAULT 1,
    assigned_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_customer_assign_dates CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT uq_customer_assignment_overlap EXCLUDE USING gist (
        customer_org_id WITH =,
        tstzrange(effective_from, effective_to, '[)') WITH &&
    ) WHERE (status = 'active')
);

CREATE INDEX idx_customer_rate_assignments_org ON customer_rate_assignments(customer_org_id);
CREATE INDEX idx_customer_rate_assignments_card ON customer_rate_assignments(card_id);

-- 7. Customer Rate Assignments History Table
CREATE TABLE customer_rate_assignments_history (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    assignment_id       UUID NOT NULL REFERENCES customer_rate_assignments(id) ON DELETE CASCADE,
    customer_org_id     UUID NOT NULL,
    card_id             UUID NOT NULL,
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    status              VARCHAR(32) NOT NULL,
    version             INT NOT NULL,
    snapshot            JSONB NOT NULL,
    changed_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason       TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_cust_assign_hist_version UNIQUE (assignment_id, version)
);

CREATE INDEX idx_customer_rate_assignments_history_aid ON customer_rate_assignments_history(assignment_id);

-- 8. Reseller Cost Assignments Table
CREATE TABLE reseller_cost_assignments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    reseller_org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    card_id             UUID NOT NULL REFERENCES rate_cards(id) ON DELETE RESTRICT,
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    status              VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'superseded')),
    version             INT NOT NULL DEFAULT 1,
    assigned_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_reseller_cost_assign_dates CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT uq_reseller_cost_assignment_overlap EXCLUDE USING gist (
        reseller_org_id WITH =,
        tstzrange(effective_from, effective_to, '[)') WITH &&
    ) WHERE (status = 'active')
);

CREATE INDEX idx_reseller_cost_assignments_org ON reseller_cost_assignments(reseller_org_id);
CREATE INDEX idx_reseller_cost_assignments_card ON reseller_cost_assignments(card_id);

-- 9. Reseller Cost Assignments History Table
CREATE TABLE reseller_cost_assignments_history (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    assignment_id       UUID NOT NULL REFERENCES reseller_cost_assignments(id) ON DELETE CASCADE,
    reseller_org_id     UUID NOT NULL,
    card_id             UUID NOT NULL,
    effective_from      TIMESTAMPTZ NOT NULL,
    effective_to        TIMESTAMPTZ,
    status              VARCHAR(32) NOT NULL,
    version             INT NOT NULL,
    snapshot            JSONB NOT NULL,
    changed_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    changed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    change_reason       TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_reseller_cost_assign_hist_version UNIQUE (assignment_id, version)
);

CREATE INDEX idx_reseller_cost_assignments_history_aid ON reseller_cost_assignments_history(assignment_id);

-- 10. Extend change_requests and import_jobs CHECK constraints for Phase 5 entity types
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment', 'reseller_cost_assignment'
    ));

ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_entity_type_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'provider_coverage', 'sender_id',
        'rate_card_rate', 'customer_sell_rate'
    ));


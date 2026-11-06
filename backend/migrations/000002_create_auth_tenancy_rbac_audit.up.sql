-- Step 1: Organizations (Tenancy)
CREATE TABLE organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255) NOT NULL,
    type        VARCHAR(32) NOT NULL CHECK (type IN ('platform', 'reseller', 'customer')),
    parent_id   UUID REFERENCES organizations(id) ON DELETE RESTRICT,
    path        TEXT NOT NULL UNIQUE,
    status      VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Exactly one platform organization in the entire platform
CREATE UNIQUE INDEX idx_organizations_single_platform ON organizations (type) WHERE type = 'platform';
CREATE INDEX idx_organizations_path ON organizations (path);
CREATE INDEX idx_organizations_parent_id ON organizations (parent_id);
CREATE INDEX idx_organizations_status ON organizations (status);

-- Step 2: Users
CREATE TABLE users (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    email                   VARCHAR(255) NOT NULL UNIQUE,
    password_hash           TEXT,
    status                  VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'invited')),
    mfa_enabled             BOOLEAN NOT NULL DEFAULT false,
    mfa_secret_encrypted    TEXT,
    mfa_enrolled_at         TIMESTAMPTZ,
    failed_login_attempts   INT NOT NULL DEFAULT 0,
    locked_until            TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_org_id ON users (organization_id);
CREATE INDEX idx_users_status ON users (status);

-- Recovery Codes for TOTP MFA
CREATE TABLE user_recovery_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash   VARCHAR(64) NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_recovery_codes_user_id ON user_recovery_codes (user_id);

-- Step 3: Permissions (Code-Defined)
CREATE TABLE permissions (
    code        VARCHAR(128) PRIMARY KEY,
    module      VARCHAR(64) NOT NULL,
    resource    VARCHAR(64) NOT NULL,
    action      VARCHAR(64) NOT NULL,
    description TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed System Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('auth.session.view', 'auth', 'session', 'view', 'View active authentication sessions'),
    ('auth.session.revoke', 'auth', 'session', 'revoke', 'Revoke user sessions'),
    ('auth.mfa.manage', 'auth', 'mfa', 'manage', 'Configure or reset MFA for users'),
    ('auth.password.reset', 'auth', 'password', 'reset', 'Initiate password reset for users'),
    ('org.organization.view', 'org', 'organization', 'view', 'View organization details and hierarchy'),
    ('org.organization.create', 'org', 'organization', 'create', 'Create child organizations'),
    ('org.organization.edit', 'org', 'organization', 'edit', 'Modify organization details'),
    ('org.organization.suspend', 'org', 'organization', 'suspend', 'Suspend an organization'),
    ('org.organization.activate', 'org', 'organization', 'activate', 'Reactivate a suspended organization'),
    ('user.account.view', 'user', 'account', 'view', 'View user accounts and profiles'),
    ('user.account.create', 'user', 'account', 'create', 'Create user accounts'),
    ('user.account.edit', 'user', 'account', 'edit', 'Modify user accounts and assigned roles'),
    ('user.account.suspend', 'user', 'account', 'suspend', 'Suspend user accounts'),
    ('user.account.activate', 'user', 'account', 'activate', 'Reactivate suspended user accounts'),
    ('user.account.invite', 'user', 'account', 'invite', 'Invite new users via onboarding link'),
    ('role.role.view', 'role', 'role', 'view', 'View roles and permission sets'),
    ('role.role.create', 'role', 'role', 'create', 'Create custom roles'),
    ('role.role.edit', 'role', 'role', 'edit', 'Modify custom roles and permission assignments'),
    ('role.role.delete', 'role', 'role', 'delete', 'Delete unassigned custom roles'),
    ('role.role.assign', 'role', 'role', 'assign', 'Assign roles to users'),
    ('audit.log.view', 'audit', 'log', 'view', 'View audit trail logs'),
    ('system.info.view', 'system', 'info', 'view', 'View system build and schema status');

-- Step 4: Roles & Role Permissions (Per Organization)
CREATE TABLE roles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            VARCHAR(128) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    is_system       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_roles_org_name UNIQUE (organization_id, name)
);

CREATE INDEX idx_roles_org_id ON roles (organization_id);

CREATE TABLE role_permissions (
    role_id         UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_code VARCHAR(128) NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_code)
);

CREATE TABLE user_roles (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

-- Step 5: Sessions & Tokens
CREATE TABLE sessions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    refresh_token_hash  VARCHAR(64) NOT NULL UNIQUE,
    family_id           UUID NOT NULL,
    ip_address          VARCHAR(64) NOT NULL DEFAULT '',
    user_agent          TEXT NOT NULL DEFAULT '',
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    revoked_reason      TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_family_id ON sessions (family_id);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- MFA Challenge Tokens (Short-Lived 5 min)
CREATE TABLE mfa_challenges (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    challenge_token_hash    VARCHAR(64) NOT NULL UNIQUE,
    expires_at              TIMESTAMPTZ NOT NULL,
    used_at                 TIMESTAMPTZ,
    ip_address              VARCHAR(64) NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Password Reset Tokens (Single-Use 30 min)
CREATE TABLE password_resets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  VARCHAR(64) NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- User Invitation Tokens (Single-Use 7 days)
CREATE TABLE user_invitations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  VARCHAR(64) NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Login History
CREATE TABLE login_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    organization_id UUID REFERENCES organizations(id) ON DELETE SET NULL,
    email           VARCHAR(255) NOT NULL,
    success         BOOLEAN NOT NULL,
    reason          VARCHAR(128) NOT NULL DEFAULT '',
    ip_address      VARCHAR(64) NOT NULL DEFAULT '',
    user_agent      TEXT NOT NULL DEFAULT '',
    request_id      VARCHAR(64) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_login_history_user_id ON login_history (user_id);
CREATE INDEX idx_login_history_created_at ON login_history (created_at DESC);

-- Step 6: Append-Only Immutable Audit Logs
CREATE TABLE audit_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_org_id    UUID REFERENCES organizations(id) ON DELETE SET NULL,
    target_org_id   UUID REFERENCES organizations(id) ON DELETE SET NULL,
    action          VARCHAR(128) NOT NULL,
    module          VARCHAR(64) NOT NULL,
    entity_type     VARCHAR(64) NOT NULL,
    entity_id       VARCHAR(128) NOT NULL,
    before_value    JSONB,
    after_value     JSONB,
    ip_address      VARCHAR(64) NOT NULL DEFAULT '',
    user_agent      TEXT NOT NULL DEFAULT '',
    request_id      VARCHAR(64) NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_logs_timestamp ON audit_logs (timestamp DESC);
CREATE INDEX idx_audit_logs_target_org ON audit_logs (target_org_id);
CREATE INDEX idx_audit_logs_actor_user ON audit_logs (actor_user_id);
CREATE INDEX idx_audit_logs_module ON audit_logs (module);
CREATE INDEX idx_audit_logs_action ON audit_logs (action);

-- Audit Immutability Triggers (Block UPDATE, DELETE, TRUNCATE)
CREATE OR REPLACE FUNCTION audit_logs_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: modifications and deletions are prohibited';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_logs_no_update
BEFORE UPDATE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION audit_logs_immutable();

CREATE TRIGGER trg_audit_logs_no_delete
BEFORE DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION audit_logs_immutable();

CREATE TRIGGER trg_audit_logs_no_truncate
BEFORE TRUNCATE ON audit_logs
EXECUTE FUNCTION audit_logs_immutable();

-- Revoke mutation rights from znero_app role where supported
REVOKE UPDATE, DELETE, TRUNCATE ON audit_logs FROM znero_app;

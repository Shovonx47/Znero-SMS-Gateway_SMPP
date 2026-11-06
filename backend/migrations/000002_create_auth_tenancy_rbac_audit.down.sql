DROP TRIGGER IF EXISTS trg_audit_logs_no_truncate ON audit_logs;
DROP TRIGGER IF EXISTS trg_audit_logs_no_delete ON audit_logs;
DROP TRIGGER IF EXISTS trg_audit_logs_no_update ON audit_logs;
DROP FUNCTION IF EXISTS audit_logs_immutable();

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS login_history;
DROP TABLE IF EXISTS user_invitations;
DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS mfa_challenges;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS user_recovery_codes;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS organizations;

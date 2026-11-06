ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check 
    CHECK (entity_type IN ('country', 'operator', 'prefix', 'number_rule', 'import_job'));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check 
    CHECK (operation IN ('create', 'update', 'deactivate', 'activate', 'archive', 'rollback', 'confirm_import'));

ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_status_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_status_check 
    CHECK (status IN ('pending', 'parsing', 'preview_ready', 'pending_approval', 'confirming', 'completed', 'failed', 'rolled_back'));

INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('import.job.override', 'import', 'job', 'override', 'Bypass maker-checker workflow when confirming imports')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, 'import.job.override'
FROM roles r
WHERE r.name = 'Super Admin'
ON CONFLICT DO NOTHING;

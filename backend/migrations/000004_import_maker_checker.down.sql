DELETE FROM import_jobs WHERE status NOT IN ('pending', 'parsing', 'preview_ready', 'confirming', 'completed', 'failed', 'rolled_back');
ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_status_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_status_check 
    CHECK (status IN ('pending', 'parsing', 'preview_ready', 'confirming', 'completed', 'failed', 'rolled_back'));

DELETE FROM change_requests WHERE operation NOT IN ('create', 'update', 'deactivate', 'activate', 'archive', 'rollback') OR entity_type NOT IN ('country', 'operator', 'prefix', 'number_rule');
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check 
    CHECK (operation IN ('create', 'update', 'deactivate', 'activate', 'archive', 'rollback'));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check 
    CHECK (entity_type IN ('country', 'operator', 'prefix', 'number_rule'));

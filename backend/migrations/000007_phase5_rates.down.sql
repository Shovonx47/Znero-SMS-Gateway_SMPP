-- Drop tables in reverse order of dependencies
DROP TABLE IF EXISTS reseller_cost_assignments_history CASCADE;
DROP TABLE IF EXISTS reseller_cost_assignments CASCADE;
DROP TABLE IF EXISTS customer_rate_assignments_history CASCADE;
DROP TABLE IF EXISTS customer_rate_assignments CASCADE;
DROP TABLE IF EXISTS rates_history CASCADE;
DROP TABLE IF EXISTS rates CASCADE;
DROP TABLE IF EXISTS rate_cards_history CASCADE;
DROP TABLE IF EXISTS rate_cards CASCADE;

-- Delete Phase 5 permissions and associated role_permissions
DELETE FROM role_permissions WHERE permission_code LIKE 'rate.%';
DELETE FROM permissions WHERE module = 'rate';

-- Restore Phase 4 check constraints
DELETE FROM change_requests WHERE entity_type NOT IN (
    'country', 'operator', 'prefix', 'number_rule', 'import_job',
    'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id'
);
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id'
    ));

DELETE FROM import_jobs WHERE entity_type NOT IN ('country', 'operator', 'prefix', 'provider_coverage', 'sender_id');
ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_entity_type_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_entity_type_check
    CHECK (entity_type IN ('country', 'operator', 'prefix', 'provider_coverage', 'sender_id'));


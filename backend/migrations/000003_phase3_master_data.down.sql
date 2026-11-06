-- Drop tables in reverse order of creation
DROP TABLE IF EXISTS number_transform_rules_history CASCADE;
DROP TABLE IF EXISTS number_transform_rules CASCADE;
DROP TABLE IF EXISTS import_job_rows CASCADE;
DROP TABLE IF EXISTS import_jobs CASCADE;
DROP TABLE IF EXISTS change_requests CASCADE;
DROP TABLE IF EXISTS number_prefixes_history CASCADE;
DROP TABLE IF EXISTS number_prefixes CASCADE;
DROP TABLE IF EXISTS operators_history CASCADE;
DROP TABLE IF EXISTS operators CASCADE;
DROP TABLE IF EXISTS countries_history CASCADE;
DROP TABLE IF EXISTS countries CASCADE;

-- Delete Phase 3 permissions
DELETE FROM permissions WHERE code IN (
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
);

-- Phase 7: Routing Engine Migration (DOWN)

DELETE FROM role_permissions WHERE permission_code LIKE 'routing.%';
DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE name IN ('Routing Maker', 'Routing Checker'));
DELETE FROM user_roles WHERE role_id IN (SELECT id FROM roles WHERE name IN ('Routing Maker', 'Routing Checker'));
DELETE FROM roles WHERE name IN ('Routing Maker', 'Routing Checker');
DELETE FROM permissions WHERE module = 'routing';

-- Clean up routing change requests before reverting constraints
DELETE FROM change_requests WHERE entity_type IN (
    'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_plan_entry',
    'route_assignment_change', 'customer_route_assignment', 'routing_rule_change', 'routing_rule',
    'connection_reenable', 'provider_connection_enable'
);

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment', 'reseller_cost_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void'
    ));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check
    CHECK (operation IN (
        'create', 'update', 'deactivate', 'activate', 'archive', 'rollback',
        'confirm_import', 'status_change', 'secret_update', 'approve_sender_id',
        'topup', 'adjustment', 'unfreeze', 'void'
    ));

DROP TABLE IF EXISTS provider_connection_overrides CASCADE;
DROP TABLE IF EXISTS routing_rule_history CASCADE;
DROP TABLE IF EXISTS routing_rules CASCADE;
DROP TABLE IF EXISTS customer_route_assignment_history CASCADE;
DROP TABLE IF EXISTS customer_route_assignments CASCADE;
DROP TABLE IF EXISTS route_plan_entry_history CASCADE;
DROP TABLE IF EXISTS route_plan_entries CASCADE;
DROP TABLE IF EXISTS route_plan_history CASCADE;
DROP TABLE IF EXISTS route_plans CASCADE;

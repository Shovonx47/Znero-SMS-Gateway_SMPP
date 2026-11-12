-- Revert Phase 8: Messaging Engine Migration (000010)

-- 1. Remove Permissions
DELETE FROM role_permissions WHERE permission_code LIKE 'message.%';
DELETE FROM permissions WHERE module = 'messaging';

-- 2. Revert change_requests constraint
DELETE FROM change_requests WHERE entity_type NOT IN (
    'country', 'operator', 'prefix', 'number_rule', 'import_job',
    'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
    'rate_card', 'rate', 'rate_assignment',
    'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
    'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable'
);
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable'
    ));

-- 3. Drop Triggers & Function
DROP TRIGGER IF EXISTS trg_message_dlrs_immutable ON message_dlrs;
DROP TRIGGER IF EXISTS trg_message_events_immutable ON message_events;
DROP FUNCTION IF EXISTS block_messaging_append_only_mutation();

-- 4. Drop Tables
DROP TABLE IF EXISTS message_dlrs CASCADE;
DROP TABLE IF EXISTS message_events CASCADE;
DROP TABLE IF EXISTS message_attempts CASCADE;
DROP TABLE IF EXISTS message_outbox CASCADE;
DROP TABLE IF EXISTS message_provider_lookups CASCADE;
DROP TABLE IF EXISTS message_idempotency_lookups CASCADE;
DROP TABLE IF EXISTS message_parts CASCADE;
DROP TABLE IF EXISTS messages CASCADE;
DROP TABLE IF EXISTS system_settings CASCADE;


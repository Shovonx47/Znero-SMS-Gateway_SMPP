-- Rollback Migration 000012: Phase 8B Customer Edge, SMPP Server, Webhooks, and Inbound MO

DELETE FROM role_permissions WHERE permission_code IN (
    'customer.webhook.create', 'customer.webhook.read', 'customer.webhook.update', 'customer.webhook.delete',
    'customer.usage.view', 'customer.smppsession.view', 'customer.smppsession.disconnect',
    'inbound.number.create', 'inbound.number.read', 'inbound.number.update', 'inbound.number.delete',
    'inbound.message.view'
);

DELETE FROM permissions WHERE code IN (
    'customer.webhook.create', 'customer.webhook.read', 'customer.webhook.update', 'customer.webhook.delete',
    'customer.usage.view', 'customer.smppsession.view', 'customer.smppsession.disconnect',
    'inbound.number.create', 'inbound.number.read', 'inbound.number.update', 'inbound.number.delete',
    'inbound.message.view'
);

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable',
        'system_setting', 'dlr_mapping'
    ));

DROP TABLE IF EXISTS inbound_messages;
DROP TABLE IF EXISTS inbound_numbers_history;
DROP TABLE IF EXISTS inbound_numbers;
DROP TABLE IF EXISTS customer_webhook_attempts;
DROP TABLE IF EXISTS customer_webhook_deliveries;
DROP TABLE IF EXISTS customer_webhooks_history;
DROP TABLE IF EXISTS customer_webhooks;
DROP TABLE IF EXISTS smpp_dlr_queue;
DROP TABLE IF EXISTS customer_smpp_sessions;
DROP TABLE IF EXISTS customer_usage_counters;
DROP TABLE IF EXISTS message_locator;

ALTER TABLE messages 
    DROP COLUMN IF EXISTS dlr_requested,
    DROP COLUMN IF EXISTS ingress_account_id,
    DROP COLUMN IF EXISTS ingress_channel;

ALTER TABLE message_idempotency_lookups 
    DROP COLUMN IF EXISTS request_fingerprint;

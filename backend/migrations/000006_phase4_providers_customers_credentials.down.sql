DROP TABLE IF EXISTS sender_ids_history;
DROP TABLE IF EXISTS sender_ids;
DROP TABLE IF EXISTS customer_smpp_accounts;
DROP TABLE IF EXISTS customer_api_keys;
DROP TABLE IF EXISTS customer_profiles_history;
DROP TABLE IF EXISTS customer_profiles;
DROP TABLE IF EXISTS provider_coverage_history;
DROP TABLE IF EXISTS provider_coverage;
DROP TABLE IF EXISTS provider_connections_history;
DROP TABLE IF EXISTS provider_connections;
DROP TABLE IF EXISTS providers_history;
DROP TABLE IF EXISTS providers;

DELETE FROM role_permissions WHERE permission_code LIKE 'provider.%' OR permission_code LIKE 'customer.%';
DELETE FROM permissions WHERE module IN ('provider', 'customer');

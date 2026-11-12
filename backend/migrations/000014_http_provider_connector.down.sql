-- Rollback Migration 000014: HTTP Provider Connector Configuration & DLR Support

ALTER TABLE provider_connections
    DROP COLUMN IF EXISTS http_dlr_secret_changed_at,
    DROP COLUMN IF EXISTS http_dlr_secret_encrypted,
    DROP COLUMN IF EXISTS http_dlr_mode,
    DROP COLUMN IF EXISTS http_config;

UPDATE provider_connections
SET http_auth_type = 'header_token'
WHERE http_auth_type IN ('bearer', 'api_key_header');

UPDATE provider_connections
SET http_auth_type = 'query_token'
WHERE http_auth_type = 'api_key_query';

ALTER TABLE provider_connections
    DROP CONSTRAINT IF EXISTS provider_connections_http_auth_type_check;

ALTER TABLE provider_connections
    ADD CONSTRAINT provider_connections_http_auth_type_check
    CHECK (http_auth_type IN ('none', 'basic', 'header_token', 'query_token'));


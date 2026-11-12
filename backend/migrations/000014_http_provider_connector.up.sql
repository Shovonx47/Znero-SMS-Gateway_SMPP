-- Migration 000014: HTTP Provider Connector Configuration & DLR Support

ALTER TABLE provider_connections
    DROP CONSTRAINT IF EXISTS provider_connections_http_auth_type_check;

ALTER TABLE provider_connections
    ADD CONSTRAINT provider_connections_http_auth_type_check
    CHECK (http_auth_type IN (
        'none',
        'basic',
        'bearer',
        'header_token',
        'api_key_header',
        'query_token',
        'api_key_query'
    ));

ALTER TABLE provider_connections
    ADD COLUMN IF NOT EXISTS http_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS http_dlr_mode VARCHAR(16) NOT NULL DEFAULT 'none'
        CHECK (http_dlr_mode IN ('none', 'callback', 'polling')),
    ADD COLUMN IF NOT EXISTS http_dlr_secret_encrypted TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS http_dlr_secret_changed_at TIMESTAMPTZ;


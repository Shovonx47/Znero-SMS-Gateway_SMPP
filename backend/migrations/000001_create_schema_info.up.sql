CREATE TABLE schema_info (
    key        text        PRIMARY KEY,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT schema_info_key_format CHECK (key ~ '^[a-z][a-z0-9_.]{0,62}$'),
    CONSTRAINT schema_info_value_length CHECK (char_length(value) BETWEEN 1 AND 500)
);

COMMENT ON TABLE schema_info IS
    'Global (not tenant-scoped) metadata about this database schema. Contains no business data.';

-- Real, system-generated record: when this database schema was first created.
INSERT INTO schema_info (key, value)
VALUES ('schema_initialized_at', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'));

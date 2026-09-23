CREATE TABLE server_groups (
    id text PRIMARY KEY CHECK (id <> ''),
    name text NOT NULL UNIQUE CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Keep the identifiers already used by published script distribution rules.
INSERT INTO server_groups (id, name)
SELECT DISTINCT server_group_id, server_group_id FROM servers WHERE server_group_id <> '';

INSERT INTO schema_migrations (version) VALUES (16) ON CONFLICT (version) DO NOTHING;

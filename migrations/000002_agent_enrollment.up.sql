ALTER TABLE servers
    ADD COLUMN last_heartbeat_sequence bigint NOT NULL DEFAULT 0 CHECK (last_heartbeat_sequence >= 0);

ALTER TABLE server_snapshots
    ADD COLUMN cpu_used_milli bigint NOT NULL DEFAULT 0 CHECK (cpu_used_milli >= 0),
    ADD COLUMN memory_used_bytes bigint NOT NULL DEFAULT 0 CHECK (memory_used_bytes >= 0),
    ADD COLUMN disk_free_bytes bigint NOT NULL DEFAULT 0 CHECK (disk_free_bytes >= 0);

CREATE TABLE server_enrollment_tokens (
    id uuid PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    server_name text NOT NULL,
    cloud_provider text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX server_enrollment_tokens_active_idx
    ON server_enrollment_tokens (expires_at)
    WHERE used_at IS NULL;

CREATE TABLE agent_identities (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    credential_hash bytea NOT NULL UNIQUE,
    last_authenticated_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

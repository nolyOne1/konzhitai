CREATE TABLE backup_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_type text NOT NULL CHECK (trigger_type IN ('scheduled', 'manual')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'exporting', 'snapshotting', 'uploading', 'succeeded', 'degraded', 'failed')),
    scheduled_for timestamptz,
    idempotency_key text,
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    local_snapshot_id text NOT NULL DEFAULT '',
    cos_snapshot_id text NOT NULL DEFAULT '',
    manifest_sha256 text NOT NULL DEFAULT '',
    byte_size bigint NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    object_count bigint NOT NULL DEFAULT 0 CHECK (object_count >= 0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scheduled_for)
);

CREATE UNIQUE INDEX backup_runs_idempotency_idx ON backup_runs (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX backup_runs_active_lease_idx ON backup_runs ((true))
    WHERE status IN ('exporting', 'snapshotting', 'uploading') AND lease_until IS NOT NULL;
CREATE INDEX backup_runs_due_idx ON backup_runs (next_attempt_at, created_at)
    WHERE status IN ('queued', 'degraded');

CREATE TABLE restore_verifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_run_id uuid NOT NULL REFERENCES backup_runs(id) ON DELETE RESTRICT,
    trigger_type text NOT NULL CHECK (trigger_type IN ('scheduled', 'manual')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'restoring', 'checking', 'succeeded', 'failed')),
    scheduled_for timestamptz,
    idempotency_key text,
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    temporary_database text NOT NULL DEFAULT '',
    migration_version text NOT NULL DEFAULT '',
    checked_objects bigint NOT NULL DEFAULT 0 CHECK (checked_objects >= 0),
    lease_until timestamptz,
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scheduled_for)
);

CREATE UNIQUE INDEX restore_verifications_idempotency_idx
    ON restore_verifications (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX restore_verifications_active_lease_idx ON restore_verifications ((true))
    WHERE status IN ('restoring', 'checking') AND lease_until IS NOT NULL;
CREATE INDEX restore_verifications_due_idx ON restore_verifications (created_at)
    WHERE status='queued';

INSERT INTO schema_migrations (version) VALUES (12)
ON CONFLICT (version) DO NOTHING;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email));

CREATE TABLE roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    permissions jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, role_id)
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE servers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    cloud_provider text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'online', 'offline', 'draining', 'quarantined')),
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    runtimes jsonb NOT NULL DEFAULT '[]'::jsonb,
    max_concurrency integer NOT NULL DEFAULT 1 CHECK (max_concurrency > 0),
    agent_version text NOT NULL DEFAULT '',
    identity_fingerprint text UNIQUE,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE server_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    cpu_usage_percent double precision NOT NULL CHECK (cpu_usage_percent BETWEEN 0 AND 100),
    memory_total_bytes bigint NOT NULL CHECK (memory_total_bytes >= 0),
    memory_available_bytes bigint NOT NULL CHECK (memory_available_bytes >= 0),
    disk_total_bytes bigint NOT NULL CHECK (disk_total_bytes >= 0),
    disk_available_bytes bigint NOT NULL CHECK (disk_available_bytes >= 0),
    running_tasks integer NOT NULL DEFAULT 0 CHECK (running_tasks >= 0),
    collected_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX server_snapshots_server_collected_idx ON server_snapshots (server_id, collected_at DESC);

CREATE TABLE scripts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    runtime text NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE script_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    script_id uuid NOT NULL REFERENCES scripts(id) ON DELETE RESTRICT,
    version integer NOT NULL CHECK (version > 0),
    artifact_uri text NOT NULL,
    artifact_sha256 text NOT NULL CHECK (length(artifact_sha256) = 64),
    entrypoint text NOT NULL,
    manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
    release_notes text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (script_id, version)
);

CREATE TABLE script_drafts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    script_id uuid NOT NULL UNIQUE REFERENCES scripts(id) ON DELETE CASCADE,
    base_version_id uuid REFERENCES script_versions(id) ON DELETE SET NULL,
    content text NOT NULL DEFAULT '',
    manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE script_syncs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    script_version_id uuid NOT NULL REFERENCES script_versions(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'syncing', 'synced', 'failed', 'drifted')),
    artifact_sha256 text,
    error_message text NOT NULL DEFAULT '',
    synced_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, script_version_id)
);

CREATE TABLE secrets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    key_version integer NOT NULL CHECK (key_version > 0),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE task_definitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    script_id uuid NOT NULL REFERENCES scripts(id) ON DELETE RESTRICT,
    version_policy text NOT NULL DEFAULT 'latest' CHECK (version_policy IN ('latest', 'pinned')),
    pinned_version_id uuid REFERENCES script_versions(id) ON DELETE RESTRICT,
    enabled boolean NOT NULL DEFAULT true,
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_bindings jsonb NOT NULL DEFAULT '{}'::jsonb,
    required_labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    required_runtime text NOT NULL,
    cpu_millicores integer NOT NULL DEFAULT 100 CHECK (cpu_millicores > 0),
    memory_bytes bigint NOT NULL DEFAULT 134217728 CHECK (memory_bytes > 0),
    disk_bytes bigint NOT NULL DEFAULT 134217728 CHECK (disk_bytes > 0),
    timeout_seconds integer NOT NULL DEFAULT 3600 CHECK (timeout_seconds > 0),
    max_retries integer NOT NULL DEFAULT 0 CHECK (max_retries >= 0),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((version_policy = 'latest' AND pinned_version_id IS NULL) OR (version_policy = 'pinned' AND pinned_version_id IS NOT NULL))
);

CREATE TABLE task_schedules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_definition_id uuid NOT NULL REFERENCES task_definitions(id) ON DELETE CASCADE,
    cron_expression text NOT NULL,
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    enabled boolean NOT NULL DEFAULT true,
    next_run_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE task_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_definition_id uuid NOT NULL REFERENCES task_definitions(id) ON DELETE RESTRICT,
    script_version_id uuid NOT NULL REFERENCES script_versions(id) ON DELETE RESTRICT,
    assigned_server_id uuid REFERENCES servers(id) ON DELETE SET NULL,
    requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
    trigger_type text NOT NULL CHECK (trigger_type IN ('manual', 'schedule', 'retry')),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'scheduling', 'assigned', 'syncing', 'running', 'succeeded', 'failed', 'timed_out', 'cancelled', 'expired', 'unknown')),
    parameters_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    scheduled_for timestamptz,
    queued_at timestamptz NOT NULL DEFAULT now(),
    assigned_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    exit_code integer,
    attempt integer NOT NULL DEFAULT 1 CHECK (attempt > 0),
    result_summary text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX task_runs_schedule_unique ON task_runs (task_definition_id, scheduled_for) WHERE scheduled_for IS NOT NULL;
CREATE INDEX task_runs_queue_idx ON task_runs (state, queued_at) WHERE state IN ('queued', 'scheduling');

CREATE TABLE resource_leases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_run_id uuid NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    cpu_millicores integer NOT NULL CHECK (cpu_millicores > 0),
    memory_bytes bigint NOT NULL CHECK (memory_bytes > 0),
    disk_bytes bigint NOT NULL CHECK (disk_bytes > 0),
    expires_at timestamptz NOT NULL,
    released_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX resource_leases_active_run_unique ON resource_leases (task_run_id) WHERE released_at IS NULL;
CREATE INDEX resource_leases_server_active_idx ON resource_leases (server_id, expires_at) WHERE released_at IS NULL;

CREATE TABLE run_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_run_id uuid NOT NULL REFERENCES task_runs(id) ON DELETE RESTRICT,
    sequence bigint NOT NULL CHECK (sequence >= 0),
    event_type text NOT NULL,
    state text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_run_id, sequence)
);

CREATE TABLE log_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_run_id uuid NOT NULL REFERENCES task_runs(id) ON DELETE RESTRICT,
    stream text NOT NULL CHECK (stream IN ('stdout', 'stderr', 'system')),
    sequence bigint NOT NULL CHECK (sequence >= 0),
    content text NOT NULL,
    byte_size integer NOT NULL CHECK (byte_size >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_run_id, stream, sequence)
);

CREATE TABLE audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip_address inet,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_target_idx ON audit_logs (target_type, target_id, created_at DESC);

CREATE FUNCTION reject_immutable_row_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '历史记录只允许追加，禁止更新或删除';
END;
$$;

CREATE TRIGGER script_versions_immutable
BEFORE UPDATE OR DELETE ON script_versions
FOR EACH ROW EXECUTE FUNCTION reject_immutable_row_change();

CREATE TRIGGER run_events_immutable
BEFORE UPDATE OR DELETE ON run_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_row_change();

CREATE TRIGGER log_chunks_immutable
BEFORE UPDATE OR DELETE ON log_chunks
FOR EACH ROW EXECUTE FUNCTION reject_immutable_row_change();

CREATE TRIGGER audit_logs_immutable
BEFORE UPDATE OR DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION reject_immutable_row_change();

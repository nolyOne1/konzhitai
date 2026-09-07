ALTER TABLE servers
    ADD COLUMN agent_os text NOT NULL DEFAULT '',
    ADD COLUMN agent_arch text NOT NULL DEFAULT '',
    ADD COLUMN agent_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE agent_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'available'
        CHECK (status IN ('available', 'withdrawn')),
    recommended boolean NOT NULL DEFAULT false,
    release_notes text NOT NULL DEFAULT '',
    manifest_sha256 char(64) NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_releases_one_recommended_idx
    ON agent_releases ((recommended))
    WHERE recommended;

CREATE TABLE agent_release_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    release_id uuid NOT NULL REFERENCES agent_releases(id) ON DELETE RESTRICT,
    os text NOT NULL CHECK (os = 'linux'),
    arch text NOT NULL CHECK (arch IN ('amd64', 'arm64')),
    file_name text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size > 0),
    sha256 char(64) NOT NULL,
    object_key text NOT NULL UNIQUE,
    UNIQUE (release_id, os, arch),
    UNIQUE (release_id, file_name)
);

CREATE TABLE agent_upgrade_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    target_release_id uuid NOT NULL REFERENCES agent_releases(id) ON DELETE RESTRICT,
    status text NOT NULL
        CHECK (status IN ('pending', 'running', 'paused', 'succeeded', 'cancelled')),
    first_batch_size integer NOT NULL DEFAULT 1 CHECK (first_batch_size = 1),
    batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 100),
    drain_timeout_seconds integer NOT NULL CHECK (drain_timeout_seconds BETWEEN 60 AND 86400),
    reconnect_timeout_seconds integer NOT NULL CHECK (reconnect_timeout_seconds BETWEEN 30 AND 3600),
    verification_seconds integer NOT NULL CHECK (verification_seconds BETWEEN 10 AND 600),
    current_batch integer NOT NULL DEFAULT 1 CHECK (current_batch > 0),
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    cancel_requested boolean NOT NULL DEFAULT false,
    created_by uuid NOT NULL REFERENCES users(id),
    pause_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);

CREATE UNIQUE INDEX agent_upgrade_plans_one_active_idx
    ON agent_upgrade_plans ((true))
    WHERE status IN ('pending', 'running', 'paused');

CREATE TABLE agent_upgrade_targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id uuid NOT NULL REFERENCES agent_upgrade_plans(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    batch_number integer NOT NULL CHECK (batch_number > 0),
    source_version text NOT NULL,
    target_version text NOT NULL,
    source_draining boolean NOT NULL,
    status text NOT NULL
        CHECK (status IN (
            'waiting', 'draining', 'downloading', 'verifying', 'installing',
            'reconnecting', 'health_checking', 'succeeded', 'rolling_back',
            'rolled_back', 'manual_intervention', 'cancelled'
        )),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    command_id uuid NOT NULL DEFAULT gen_random_uuid(),
    install_command_id uuid NOT NULL DEFAULT gen_random_uuid(),
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (plan_id, server_id)
);

CREATE INDEX agent_upgrade_targets_plan_batch_idx
    ON agent_upgrade_targets (plan_id, batch_number, status);

CREATE TABLE agent_upgrade_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id uuid NOT NULL REFERENCES agent_upgrade_plans(id) ON DELETE CASCADE,
    target_id uuid NOT NULL REFERENCES agent_upgrade_targets(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    command_id uuid NOT NULL,
    stage text NOT NULL,
    error_code text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agent_upgrade_events_target_time_idx
    ON agent_upgrade_events (target_id, occurred_at, id);

CREATE UNIQUE INDEX agent_upgrade_events_command_stage_idx
    ON agent_upgrade_events (target_id, command_id, stage);

INSERT INTO schema_migrations (version) VALUES (14)
ON CONFLICT (version) DO NOTHING;

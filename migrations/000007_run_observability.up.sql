ALTER TABLE task_runs
    ADD COLUMN execution_token text,
    ADD COLUMN process_confirmed_gone boolean NOT NULL DEFAULT false,
    ADD COLUMN retry_of uuid REFERENCES task_runs(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX task_runs_execution_token_unique
    ON task_runs (execution_token)
    WHERE execution_token IS NOT NULL;

ALTER TABLE run_events
    ADD COLUMN execution_token text,
    ADD COLUMN agent_sequence bigint CHECK (agent_sequence > 0);

CREATE UNIQUE INDEX run_events_agent_sequence_unique
    ON run_events (task_run_id, execution_token, agent_sequence)
    WHERE execution_token IS NOT NULL AND agent_sequence IS NOT NULL;

ALTER TABLE log_chunks
    DROP CONSTRAINT log_chunks_task_run_id_stream_sequence_key,
    ADD COLUMN execution_token text NOT NULL DEFAULT '',
    ADD CONSTRAINT log_chunks_execution_sequence_unique
        UNIQUE (task_run_id, execution_token, stream, sequence);

CREATE TABLE run_log_archives (
    task_run_id uuid PRIMARY KEY REFERENCES task_runs(id) ON DELETE RESTRICT,
    object_key text NOT NULL UNIQUE,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    sha256 text NOT NULL CHECK (length(sha256) = 64),
    first_log_at timestamptz,
    last_log_at timestamptz,
    archived_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE run_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_run_id uuid NOT NULL REFERENCES task_runs(id) ON DELETE RESTRICT,
    name text NOT NULL,
    object_key text NOT NULL UNIQUE,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    sha256 text NOT NULL CHECK (length(sha256) = 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_run_id, name)
);

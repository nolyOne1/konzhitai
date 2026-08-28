ALTER TABLE task_definitions
    ADD COLUMN priority integer NOT NULL DEFAULT 50 CHECK (priority BETWEEN 0 AND 100),
    ADD COLUMN max_concurrency integer NOT NULL DEFAULT 1 CHECK (max_concurrency > 0),
    ADD COLUMN max_wait_seconds integer NOT NULL DEFAULT 86400 CHECK (max_wait_seconds > 0),
    ADD COLUMN retry_backoff_seconds integer NOT NULL DEFAULT 30 CHECK (retry_backoff_seconds >= 0),
    ADD COLUMN idempotent boolean NOT NULL DEFAULT false;

ALTER TABLE task_runs
    ADD COLUMN priority integer NOT NULL DEFAULT 50 CHECK (priority BETWEEN 0 AND 100),
    ADD COLUMN cpu_millicores integer NOT NULL DEFAULT 100 CHECK (cpu_millicores > 0),
    ADD COLUMN memory_bytes bigint NOT NULL DEFAULT 134217728 CHECK (memory_bytes > 0),
    ADD COLUMN disk_bytes bigint NOT NULL DEFAULT 134217728 CHECK (disk_bytes > 0),
    ADD COLUMN max_concurrency integer NOT NULL DEFAULT 1 CHECK (max_concurrency > 0),
    ADD COLUMN timeout_seconds integer NOT NULL DEFAULT 3600 CHECK (timeout_seconds > 0),
    ADD COLUMN max_wait_seconds integer NOT NULL DEFAULT 86400 CHECK (max_wait_seconds > 0),
    ADD COLUMN max_retries integer NOT NULL DEFAULT 0 CHECK (max_retries >= 0),
    ADD COLUMN retry_backoff_seconds integer NOT NULL DEFAULT 30 CHECK (retry_backoff_seconds >= 0),
    ADD COLUMN idempotent boolean NOT NULL DEFAULT false;

CREATE INDEX task_schedules_due_idx
    ON task_schedules (next_run_at)
    WHERE enabled = true;

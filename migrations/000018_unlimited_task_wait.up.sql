ALTER TABLE task_definitions
    DROP CONSTRAINT task_definitions_max_wait_seconds_check,
    ADD CONSTRAINT task_definitions_max_wait_seconds_check CHECK (max_wait_seconds >= 0),
    ALTER COLUMN max_wait_seconds SET DEFAULT 0;

ALTER TABLE task_runs
    DROP CONSTRAINT task_runs_max_wait_seconds_check,
    ADD CONSTRAINT task_runs_max_wait_seconds_check CHECK (max_wait_seconds >= 0),
    ALTER COLUMN max_wait_seconds SET DEFAULT 0;

INSERT INTO schema_migrations (version) VALUES (18) ON CONFLICT (version) DO NOTHING;

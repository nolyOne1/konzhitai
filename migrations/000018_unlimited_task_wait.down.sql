-- Refuse a lossy downgrade while unlimited-wait definitions or snapshots exist.
-- Operators must explicitly choose finite limits before reverting this schema.
DELETE FROM schema_migrations WHERE version = 18;
ALTER TABLE task_definitions
    DROP CONSTRAINT task_definitions_max_wait_seconds_check,
    ADD CONSTRAINT task_definitions_max_wait_seconds_check CHECK (max_wait_seconds > 0),
    ALTER COLUMN max_wait_seconds SET DEFAULT 86400;

ALTER TABLE task_runs
    DROP CONSTRAINT task_runs_max_wait_seconds_check,
    ADD CONSTRAINT task_runs_max_wait_seconds_check CHECK (max_wait_seconds > 0),
    ALTER COLUMN max_wait_seconds SET DEFAULT 86400;

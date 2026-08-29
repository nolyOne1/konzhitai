DROP INDEX IF EXISTS task_runs_dispatch_due_idx;

ALTER TABLE task_runs
    DROP COLUMN IF EXISTS dispatch_error,
    DROP COLUMN IF EXISTS last_dispatch_at,
    DROP COLUMN IF EXISTS dispatch_attempts;

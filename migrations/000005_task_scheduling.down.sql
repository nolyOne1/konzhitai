DROP INDEX IF EXISTS task_schedules_due_idx;

ALTER TABLE task_runs
    DROP COLUMN IF EXISTS idempotent,
    DROP COLUMN IF EXISTS retry_backoff_seconds,
    DROP COLUMN IF EXISTS max_retries,
    DROP COLUMN IF EXISTS max_wait_seconds,
    DROP COLUMN IF EXISTS timeout_seconds,
    DROP COLUMN IF EXISTS max_concurrency,
    DROP COLUMN IF EXISTS disk_bytes,
    DROP COLUMN IF EXISTS memory_bytes,
    DROP COLUMN IF EXISTS cpu_millicores,
    DROP COLUMN IF EXISTS priority;

ALTER TABLE task_definitions
    DROP COLUMN IF EXISTS idempotent,
    DROP COLUMN IF EXISTS retry_backoff_seconds,
    DROP COLUMN IF EXISTS max_wait_seconds,
    DROP COLUMN IF EXISTS max_concurrency,
    DROP COLUMN IF EXISTS priority;

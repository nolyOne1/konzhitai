DROP INDEX IF EXISTS task_runs_queue_priority_idx;

ALTER TABLE task_runs
    DROP COLUMN IF EXISTS required_runtime,
    DROP COLUMN IF EXISTS required_labels;

ALTER TABLE server_snapshots
    DROP COLUMN IF EXISTS cpu_total_milli;

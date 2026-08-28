ALTER TABLE server_snapshots
    ADD COLUMN cpu_total_milli bigint NOT NULL DEFAULT 0
        CHECK (cpu_total_milli >= 0);

ALTER TABLE task_runs
    ADD COLUMN required_labels jsonb,
    ADD COLUMN required_runtime text;

UPDATE task_runs AS run
SET required_labels = definition.required_labels,
    required_runtime = definition.required_runtime
FROM task_definitions AS definition
WHERE definition.id = run.task_definition_id;

ALTER TABLE task_runs
    ALTER COLUMN required_labels SET NOT NULL,
    ALTER COLUMN required_runtime SET NOT NULL;

CREATE INDEX task_runs_queue_priority_idx
    ON task_runs (priority DESC, queued_at, id)
    WHERE state = 'queued';

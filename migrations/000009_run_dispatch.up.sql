ALTER TABLE task_runs
    ADD COLUMN dispatch_attempts integer NOT NULL DEFAULT 0 CHECK (dispatch_attempts >= 0),
    ADD COLUMN last_dispatch_at timestamptz,
    ADD COLUMN dispatch_error text NOT NULL DEFAULT '';

CREATE INDEX task_runs_dispatch_due_idx
    ON task_runs (last_dispatch_at, assigned_at)
    WHERE state = 'assigned';

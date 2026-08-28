DROP TABLE IF EXISTS run_artifacts;
DROP TABLE IF EXISTS run_log_archives;
DROP INDEX IF EXISTS run_events_agent_sequence_unique;
ALTER TABLE log_chunks
    DROP CONSTRAINT IF EXISTS log_chunks_execution_sequence_unique,
    DROP COLUMN IF EXISTS execution_token,
    ADD CONSTRAINT log_chunks_task_run_id_stream_sequence_key
        UNIQUE (task_run_id, stream, sequence);
ALTER TABLE run_events
    DROP COLUMN IF EXISTS agent_sequence,
    DROP COLUMN IF EXISTS execution_token;
DROP INDEX IF EXISTS task_runs_execution_token_unique;
ALTER TABLE task_runs
    DROP COLUMN IF EXISTS retry_of,
    DROP COLUMN IF EXISTS process_confirmed_gone,
    DROP COLUMN IF EXISTS execution_token;

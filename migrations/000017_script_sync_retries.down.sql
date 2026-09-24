DROP INDEX IF EXISTS script_syncs_retry_idx;
ALTER TABLE script_syncs DROP COLUMN next_retry_at, DROP COLUMN failure_count;
DELETE FROM schema_migrations WHERE version = 17;

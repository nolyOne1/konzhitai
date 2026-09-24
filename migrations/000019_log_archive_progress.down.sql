ALTER TABLE run_log_archives DROP COLUMN chunk_count, DROP COLUMN last_log_cursor;
DROP INDEX log_chunks_archive_cursor_idx;
ALTER TABLE log_chunks DROP COLUMN received_at, DROP COLUMN archive_cursor;
DELETE FROM schema_migrations WHERE version = 19;

ALTER TABLE log_chunks
    ADD COLUMN archive_cursor bigserial,
    ADD COLUMN received_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX log_chunks_archive_cursor_idx ON log_chunks (task_run_id, archive_cursor);

ALTER TABLE run_log_archives
    ADD COLUMN last_log_cursor bigint NOT NULL DEFAULT 0 CHECK (last_log_cursor >= 0),
    ADD COLUMN chunk_count bigint NOT NULL DEFAULT 0 CHECK (chunk_count >= 0);

INSERT INTO schema_migrations (version) VALUES (19) ON CONFLICT (version) DO NOTHING;

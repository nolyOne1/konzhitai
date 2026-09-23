ALTER TABLE script_syncs
    ADD COLUMN failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    ADD COLUMN next_retry_at timestamptz;

-- Resume previously failed transfers under the bounded retry policy.
UPDATE script_syncs SET failure_count = 1, next_retry_at = now() + interval '30 seconds'
WHERE status = 'failed';

CREATE INDEX script_syncs_retry_idx ON script_syncs (server_id, next_retry_at)
    WHERE status = 'failed' AND next_retry_at IS NOT NULL;

INSERT INTO schema_migrations (version) VALUES (17) ON CONFLICT (version) DO NOTHING;

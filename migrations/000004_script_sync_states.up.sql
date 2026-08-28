ALTER TABLE script_syncs DROP CONSTRAINT script_syncs_status_check;

UPDATE script_syncs SET status = 'downloading' WHERE status = 'syncing';
UPDATE script_syncs SET status = 'ready' WHERE status = 'synced';

ALTER TABLE script_syncs
    ADD CONSTRAINT script_syncs_status_check
    CHECK (status IN ('pending', 'downloading', 'ready', 'failed', 'drifted')),
    ADD COLUMN error_code text NOT NULL DEFAULT '';

ALTER TABLE servers
    ADD COLUMN server_group_id text NOT NULL DEFAULT '';

CREATE INDEX servers_group_idx ON servers (server_group_id) WHERE server_group_id <> '';
CREATE INDEX script_syncs_dispatch_idx
    ON script_syncs (server_id, status, updated_at)
    WHERE status IN ('pending', 'downloading', 'drifted');

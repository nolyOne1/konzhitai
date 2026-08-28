DROP INDEX IF EXISTS script_syncs_dispatch_idx;
DROP INDEX IF EXISTS servers_group_idx;

ALTER TABLE servers DROP COLUMN IF EXISTS server_group_id;

ALTER TABLE script_syncs DROP CONSTRAINT script_syncs_status_check;
UPDATE script_syncs SET status = 'syncing' WHERE status = 'downloading';
UPDATE script_syncs SET status = 'synced' WHERE status = 'ready';
ALTER TABLE script_syncs
    ADD CONSTRAINT script_syncs_status_check
    CHECK (status IN ('pending', 'syncing', 'synced', 'failed', 'drifted')),
    DROP COLUMN IF EXISTS error_code;

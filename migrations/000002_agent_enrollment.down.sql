DROP TABLE IF EXISTS agent_identities;
DROP TABLE IF EXISTS server_enrollment_tokens;

ALTER TABLE server_snapshots
    DROP COLUMN IF EXISTS disk_free_bytes,
    DROP COLUMN IF EXISTS memory_used_bytes,
    DROP COLUMN IF EXISTS cpu_used_milli;

ALTER TABLE servers
    DROP COLUMN IF EXISTS last_heartbeat_sequence;

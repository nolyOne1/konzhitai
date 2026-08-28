DROP INDEX IF EXISTS servers_schedulable_idx;

ALTER TABLE servers
    DROP COLUMN IF EXISTS scheduling_weight,
    DROP COLUMN IF EXISTS drain_requested,
    DROP COLUMN IF EXISTS enabled;

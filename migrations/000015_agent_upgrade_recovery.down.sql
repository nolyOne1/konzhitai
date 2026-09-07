DELETE FROM schema_migrations WHERE version = 15;

DROP INDEX agent_upgrade_events_command_stage_idx;

ALTER TABLE agent_upgrade_targets
    DROP COLUMN install_command_id;

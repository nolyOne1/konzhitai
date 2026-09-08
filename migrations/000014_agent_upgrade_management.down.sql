DROP TABLE agent_upgrade_events;
DROP TABLE agent_upgrade_targets;
DROP TABLE agent_upgrade_plans;
DROP TABLE agent_release_artifacts;
DROP TABLE agent_releases;

ALTER TABLE servers
    DROP COLUMN agent_capabilities,
    DROP COLUMN agent_arch,
    DROP COLUMN agent_os;

DELETE FROM schema_migrations WHERE version = 14;

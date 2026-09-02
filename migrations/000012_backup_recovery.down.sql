DELETE FROM schema_migrations WHERE version = 12;
DROP TABLE IF EXISTS restore_verifications;
DROP TABLE IF EXISTS backup_runs;

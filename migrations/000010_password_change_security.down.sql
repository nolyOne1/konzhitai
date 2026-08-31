DROP TABLE IF EXISTS auth_rate_limits;
DELETE FROM schema_migrations WHERE version = 10;
DROP TABLE IF EXISTS schema_migrations;

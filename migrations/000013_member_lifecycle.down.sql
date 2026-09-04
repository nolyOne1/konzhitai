DELETE FROM schema_migrations WHERE version = 13;
DROP INDEX IF EXISTS users_removed_at_idx;
ALTER TABLE users DROP COLUMN IF EXISTS removed_at;
ALTER TABLE users DROP COLUMN IF EXISTS must_change_password;

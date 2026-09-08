ALTER TABLE users
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false,
    ADD COLUMN removed_at timestamptz;

CREATE INDEX users_removed_at_idx ON users (removed_at, created_at, id);

INSERT INTO schema_migrations (version) VALUES (13)
ON CONFLICT (version) DO NOTHING;

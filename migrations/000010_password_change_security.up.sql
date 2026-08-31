CREATE TABLE IF NOT EXISTS schema_migrations (
    version integer PRIMARY KEY CHECK (version > 0),
    applied_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_migrations (version)
SELECT generate_series(1, 10)
ON CONFLICT (version) DO NOTHING;

CREATE TABLE auth_rate_limits (
    scope text NOT NULL CHECK (scope IN ('password_user', 'password_ip')),
    subject_hash bytea NOT NULL,
    window_started_at timestamptz NOT NULL,
    attempts integer NOT NULL CHECK (attempts > 0),
    PRIMARY KEY (scope, subject_hash)
);

CREATE INDEX auth_rate_limits_window_idx
    ON auth_rate_limits (window_started_at);

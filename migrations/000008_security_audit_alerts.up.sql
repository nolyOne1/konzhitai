ALTER TABLE secrets
    ADD COLUMN encrypted_data_key bytea,
    ADD COLUMN data_key_nonce bytea;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM secrets
        WHERE encrypted_data_key IS NULL OR data_key_nonce IS NULL
    ) THEN
        RAISE EXCEPTION '已有敏感参数必须先完成信封加密迁移';
    END IF;
END;
$$;

ALTER TABLE secrets
    ALTER COLUMN encrypted_data_key SET NOT NULL,
    ALTER COLUMN data_key_nonce SET NOT NULL;

ALTER TABLE agent_identities
    DROP CONSTRAINT agent_identities_server_id_key,
    ADD COLUMN pending_activation boolean NOT NULL DEFAULT false,
    ADD COLUMN rotated_from_id uuid REFERENCES agent_identities(id) ON DELETE SET NULL;

CREATE INDEX agent_identities_server_active_idx
    ON agent_identities (server_id, created_at DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE alerts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    source_type text NOT NULL,
    source_id text NOT NULL,
    title text NOT NULL,
    message text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved')),
    occurrence_count integer NOT NULL DEFAULT 1 CHECK (occurrence_count > 0),
    first_occurred_at timestamptz NOT NULL,
    last_occurred_at timestamptz NOT NULL,
    acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
    acknowledged_at timestamptz,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX alerts_status_last_occurred_idx
    ON alerts (status, last_occurred_at DESC);
CREATE INDEX alerts_merge_lookup_idx
    ON alerts (kind, source_type, source_id, last_occurred_at DESC)
    WHERE status = 'open';
CREATE INDEX audit_logs_actor_created_idx
    ON audit_logs (actor_id, created_at DESC);
CREATE INDEX audit_logs_action_created_idx
    ON audit_logs (action, created_at DESC);

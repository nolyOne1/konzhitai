DROP INDEX IF EXISTS audit_logs_action_created_idx;
DROP INDEX IF EXISTS audit_logs_actor_created_idx;
DROP TABLE IF EXISTS alerts;
DROP INDEX IF EXISTS agent_identities_server_active_idx;
ALTER TABLE agent_identities
    DROP COLUMN IF EXISTS rotated_from_id,
    DROP COLUMN IF EXISTS pending_activation,
    ADD CONSTRAINT agent_identities_server_id_key UNIQUE (server_id);
ALTER TABLE secrets
    DROP COLUMN IF EXISTS data_key_nonce,
    DROP COLUMN IF EXISTS encrypted_data_key;

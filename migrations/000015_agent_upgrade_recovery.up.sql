ALTER TABLE agent_upgrade_targets
    ADD COLUMN install_command_id uuid;

UPDATE agent_upgrade_targets
SET install_command_id = command_id
WHERE install_command_id IS NULL;

ALTER TABLE agent_upgrade_targets
    ALTER COLUMN install_command_id SET NOT NULL;

DELETE FROM agent_upgrade_events
WHERE id IN (
    SELECT id
    FROM (
        SELECT id, row_number() OVER (
            PARTITION BY target_id, command_id, stage
            ORDER BY occurred_at, created_at, id
        ) AS duplicate_number
        FROM agent_upgrade_events
    ) AS ranked_events
    WHERE duplicate_number > 1
);

CREATE UNIQUE INDEX agent_upgrade_events_command_stage_idx
    ON agent_upgrade_events (target_id, command_id, stage);

INSERT INTO schema_migrations(version)
VALUES (15)
ON CONFLICT (version) DO NOTHING;

ALTER TABLE servers
    ADD COLUMN enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN drain_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN scheduling_weight integer NOT NULL DEFAULT 100
        CHECK (scheduling_weight BETWEEN 1 AND 1000);

CREATE INDEX servers_schedulable_idx
    ON servers (status, scheduling_weight DESC)
    WHERE enabled = true AND drain_requested = false;

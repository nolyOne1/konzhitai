ALTER TABLE secrets
    ADD COLUMN scope text NOT NULL DEFAULT 'user'
    CHECK (scope IN ('user', 'system'));

CREATE TABLE notification_configs (
    channel text PRIMARY KEY CHECK (channel IN ('feishu')),
    enabled boolean NOT NULL DEFAULT false,
    webhook_secret_id uuid NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    signing_secret_id uuid NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    masked_destination text NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id uuid REFERENCES alerts(id) ON DELETE RESTRICT,
    event_type text NOT NULL CHECK (event_type IN ('opened', 'recovered', 'test')),
    payload jsonb NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'retrying', 'sent', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    last_error text NOT NULL DEFAULT '',
    response_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_outbox_due_idx
    ON notification_outbox (next_attempt_at, created_at)
    WHERE status IN ('pending', 'retrying', 'sending');

CREATE TABLE alert_rule_states (
    code text NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    desired_active boolean NOT NULL DEFAULT false,
    consecutive_bad integer NOT NULL DEFAULT 0 CHECK (consecutive_bad >= 0),
    consecutive_good integer NOT NULL DEFAULT 0 CHECK (consecutive_good >= 0),
    last_value double precision,
    last_evaluated_at timestamptz NOT NULL,
    PRIMARY KEY (code, source_type, source_id)
);

CREATE OR REPLACE FUNCTION enqueue_alert_notification()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    notification_event text;
    event_time timestamptz;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM notification_configs
        WHERE channel = 'feishu' AND enabled = true
    ) THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'INSERT' AND NEW.status = 'open' THEN
        notification_event := 'opened';
        event_time := NEW.last_occurred_at;
    ELSIF TG_OP = 'UPDATE' AND OLD.status <> 'resolved' AND NEW.status = 'resolved' THEN
        IF NOT EXISTS (
            SELECT 1 FROM notification_outbox
            WHERE alert_id = NEW.id AND event_type = 'opened'
        ) THEN
            RETURN NEW;
        END IF;
        notification_event := 'recovered';
        event_time := COALESCE(NEW.resolved_at, NEW.updated_at);
    ELSE
        RETURN NEW;
    END IF;

    INSERT INTO notification_outbox (
        alert_id,
        event_type,
        payload,
        idempotency_key
    ) VALUES (
        NEW.id,
        notification_event,
        jsonb_build_object(
            'code', NEW.kind,
            'severity', NEW.severity,
            'title', NEW.title,
            'sourceType', NEW.source_type,
            'sourceId', NEW.source_id,
            'occurrenceCount', NEW.occurrence_count,
            'occurredAt', event_time
        ),
        'alert:' || NEW.id::text || ':' || notification_event
    )
    ON CONFLICT (idempotency_key) DO NOTHING;

    RETURN NEW;
END;
$$;

CREATE TRIGGER alerts_enqueue_notification
AFTER INSERT OR UPDATE OF status ON alerts
FOR EACH ROW EXECUTE FUNCTION enqueue_alert_notification();

INSERT INTO schema_migrations (version) VALUES (11)
ON CONFLICT (version) DO NOTHING;

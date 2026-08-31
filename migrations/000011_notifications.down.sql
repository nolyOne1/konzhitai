DELETE FROM schema_migrations WHERE version = 11;

DROP TRIGGER IF EXISTS alerts_enqueue_notification ON alerts;
DROP FUNCTION IF EXISTS enqueue_alert_notification();
DROP TABLE IF EXISTS alert_rule_states;
DROP TABLE IF EXISTS notification_outbox;
DROP TABLE IF EXISTS notification_configs;

ALTER TABLE secrets DROP COLUMN IF EXISTS scope;

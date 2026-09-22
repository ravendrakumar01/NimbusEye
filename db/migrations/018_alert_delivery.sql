-- NimbusEye — alert delivery, mute, and the state the sender needs.
--
-- Until now the evaluator wrote a delivery intent into alert_notifications and
-- nothing sent it. Every row on the live instance reads "skipped / no notification
-- channel configured" — ninety-four of them, across five open alarms, two of which
-- are production databases at 97% and 100% storage. The tool detected all of it and
-- told nobody.
--
-- Three additions:
--
-- muted_until. Mute belongs on the alert rather than on the monitor, because that
-- is what people mean by it: stop telling me about this particular problem, keep
-- showing it to me. A muted alarm stays open and visible and queues no
-- notifications. Distinct from a maintenance window, which is planned, scoped to
-- monitors, and excluded from availability.
--
-- last_notified_at. What the repeat interval is measured from. Without it,
-- "re-notify every 60 minutes" has no anchor and would either never fire or fire
-- every pass.
--
-- An index for the sender's poll. It runs every minute looking for pending rows,
-- which is a tiny fraction of the table, and that is exactly the shape a partial
-- index serves.

BEGIN;

ALTER TABLE alerts
    ADD COLUMN IF NOT EXISTS muted_until      timestamptz,
    ADD COLUMN IF NOT EXISTS last_notified_at timestamptz;

COMMENT ON COLUMN alerts.muted_until IS
    'While in the future, this alert queues no notifications. It stays open and '
    'visible: muting is about the noise, not about the problem going away.';
COMMENT ON COLUMN alerts.last_notified_at IS
    'When a notification was last queued for this alert. The repeat interval is '
    'measured from here.';

-- The sender polls for work every minute. Pending rows are a handful out of a
-- growing table, so the index covers only those.
CREATE INDEX IF NOT EXISTS alert_notifications_pending_idx
    ON alert_notifications (tenant_id, created_at)
    WHERE state = 'pending';

-- Failed rows are retried, so they need to be findable too, bounded by attempts.
CREATE INDEX IF NOT EXISTS alert_notifications_retry_idx
    ON alert_notifications (tenant_id, created_at)
    WHERE state = 'failed';

-- A notification profile with no alert_rules routes nothing, which is the state
-- the default profile has been in since it was seeded. Give it a shape that is
-- obviously incomplete rather than silently empty: severities listed, channels
-- empty, so the Notification Profiles screen shows rows to fill in rather than a
-- blank panel that looks finished.
UPDATE notification_profiles
SET alert_rules = '[{"severity": "down", "channels": []},
                    {"severity": "critical", "channels": []},
                    {"severity": "trouble", "channels": []}]'::jsonb,
    updated_at = now()
WHERE is_default AND jsonb_array_length(alert_rules) = 0;

-- Repeats off by default. An operator turning delivery on for the first time
-- should get one message per escalation level, not a message every minute until
-- they work out where the switch is.
UPDATE notification_profiles
SET persistent_alert_interval = 0
WHERE persistent_alert_interval IS NULL;

COMMIT;

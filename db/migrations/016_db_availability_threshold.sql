-- NimbusEye — correct the Autonomous Database availability threshold.
--
-- Migration 015 added OCI_AUTONOMOUS_DB.database_availability with a percentage
-- threshold: trouble at 100, critical at 99, lower-is-worse. That was wrong, and
-- wrong in the direction that matters — it raised a critical alarm against every
-- healthy database.
--
-- OCI publishes DatabaseAvailability as an indicator, not a percentage: 1 when the
-- database is reachable, 0 when it is not. A healthy database therefore reports
-- 1.00, which is below any percentage threshold, so the rule fired constantly and
-- said nothing.
--
-- The mistake was assuming a metric named "Availability" carried a percentage.
-- The unit of a provider metric is not inferable from its name, and a threshold
-- set from a guess is worse than no threshold: it trains people to ignore alarms.
--
-- Corrected to critical below 1 with no trouble level, because an indicator has no
-- meaningful middle state.

BEGIN;

UPDATE resource_types SET metric_defs = (
    SELECT jsonb_agg(
        CASE WHEN m->>'key' = 'database_availability'
             THEN m || '{"unit": "count", "label": "Database Available",
                         "trouble": null, "critical": 0,
                         "higher_is_worse": false}'::jsonb
             ELSE m END)
    FROM jsonb_array_elements(metric_defs) m)
WHERE code = 'OCI_AUTONOMOUS_DB';

UPDATE threshold_profiles tp
SET rules = (
        SELECT jsonb_agg(
            CASE WHEN r->>'metric' = 'database_availability'
                 THEN r || '{"op": "<=", "trouble": null, "critical": 0}'::jsonb
                 ELSE r END)
        FROM jsonb_array_elements(tp.rules) r),
    updated_at = now()
WHERE tp.resource_type = 'OCI_AUTONOMOUS_DB'
  AND tp.rules @> '[{"metric": "database_availability"}]'::jsonb;

-- Clear the alarms the wrong threshold raised. They were never real, so they are
-- resolved rather than left for someone to investigate. The reason is recorded,
-- because an alarm that disappears without explanation is its own small mystery.
UPDATE alerts SET state = 'resolved', resolved_at = now(),
                  rca = jsonb_build_object(
                      'resolved_by', 'migration 016',
                      'reason', 'Raised by an incorrect threshold. OCI publishes '
                             || 'DatabaseAvailability as an indicator (1 available, '
                             || '0 not), not a percentage, so a healthy database '
                             || 'reporting 1.00 breached a threshold of 99.',
                      'false_positive', true)
WHERE metric_key = 'database_availability'
  AND state IN ('open','acknowledged')
  AND observed_value >= 1;

COMMIT;

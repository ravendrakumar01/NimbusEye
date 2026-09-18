-- NimbusEye — metric definitions for the synthetic monitor types.
--
-- WEB_REST_API, WEB_PORT, WEB_PING, WEB_DNS and WEB_HEARTBEAT were seeded with no
-- metrics, which meant a monitor of those types could only ever be up or down.
-- Response time is what turns "is it up" into "is it getting worse", and without
-- a metric definition there is nothing for a chart to draw or a threshold to
-- evaluate.
--
-- The same definitions are in internal/catalog/catalog.json, which the binaries
-- embed; this keeps the database copy in step.

BEGIN;

UPDATE resource_types rt SET metric_defs = v.defs
FROM (VALUES
    ('WEB_REST_API', '[{"key": "response_time", "label": "Response Time", "unit": "milliseconds", "provider_metric": "http_duration_ms", "namespace": "nimbuseye", "statistic": "mean", "trouble": 2000, "critical": 5000, "higher_is_worse": true}, {"key": "status_code", "label": "HTTP Status", "unit": "count", "provider_metric": "http_status", "namespace": "nimbuseye", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb),
    ('WEB_PORT', '[{"key": "connect_time", "label": "Connect Time", "unit": "milliseconds", "provider_metric": "tcp_connect_ms", "namespace": "nimbuseye", "statistic": "mean", "trouble": 2000, "critical": 5000, "higher_is_worse": true}]'::jsonb),
    ('WEB_PING', '[{"key": "round_trip_time", "label": "Round Trip Time", "unit": "milliseconds", "provider_metric": "icmp_rtt_ms", "namespace": "nimbuseye", "statistic": "mean", "trouble": 200, "critical": 500, "higher_is_worse": true}, {"key": "packet_loss", "label": "Packet Loss", "unit": "percent", "provider_metric": "icmp_loss_pct", "namespace": "nimbuseye", "statistic": "mean", "trouble": 10, "critical": 50, "higher_is_worse": true}]'::jsonb),
    ('WEB_DNS', '[{"key": "resolve_time", "label": "Resolve Time", "unit": "milliseconds", "provider_metric": "dns_resolve_ms", "namespace": "nimbuseye", "statistic": "mean", "trouble": 500, "critical": 1500, "higher_is_worse": true}]'::jsonb),
    ('WEB_HEARTBEAT', '[{"key": "since_last_beat", "label": "Time Since Last Beat", "unit": "seconds", "provider_metric": "heartbeat_age_sec", "namespace": "nimbuseye", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb)
) AS v(code, defs)
WHERE rt.code = v.code
  AND jsonb_array_length(rt.metric_defs) = 0;

-- Rebuild the default threshold profiles for those types so the new metrics are
-- actually evaluated. Without this the definitions exist but nothing alerts on
-- them, which is the quiet half-configured state this project keeps trying to
-- avoid.
UPDATE threshold_profiles tp
SET rules = COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'metric',      m->>'key',
            'op',          CASE WHEN (m->>'higher_is_worse')::boolean THEN '>=' ELSE '<=' END,
            'trouble',     m->'trouble',
            'critical',    m->'critical',
            'polls_check', 3,
            'strategy',    'consecutive'))
        FROM jsonb_array_elements(rt.metric_defs) m
        WHERE m->'trouble' <> 'null'::jsonb OR m->'critical' <> 'null'::jsonb),
    '[]'::jsonb)
FROM resource_types rt
WHERE tp.resource_type = rt.code
  AND tp.system_generated
  AND rt.code IN ('WEB_REST_API','WEB_PORT','WEB_PING','WEB_DNS','WEB_HEARTBEAT');

COMMIT;

-- NimbusEye — interim metric storage and a default tenant.
--
-- METRIC STORAGE
--
-- The design calls for VictoriaMetrics: at 2000 resources, 15 metrics each and
-- five-minute polling the estate produces roughly 8.6 million samples a day, and
-- PostgreSQL is the wrong shape for that. This table exists so the product works
-- end to end before VictoriaMetrics is provisioned, and is deliberately built to
-- be thrown away:
--
--   * partitioned by day, so old data is detached rather than deleted;
--   * no foreign key to resources, because the ingest path must not pay a
--     constraint check per sample;
--   * keyed on native_id rather than the internal resource id, so a resource
--     that is rediscovered with a new row keeps its history.
--
-- When VictoriaMetrics is wired up, PutSamples changes and this table is dropped.
--
-- DEFAULT TENANT
--
-- Every tenant-scoped query needs a tenant. Until authentication exists there is
-- no user to derive one from, so a single tenant is provisioned here and the API
-- runs against it. Multi-tenancy is already enforced in the schema; this is just
-- the first tenant, not a special case.

BEGIN;

CREATE TABLE metric_samples (
    tenant_id  uuid NOT NULL,
    native_id  text NOT NULL,
    metric_key text NOT NULL,
    t          timestamptz NOT NULL,
    v          double precision NOT NULL,
    PRIMARY KEY (tenant_id, native_id, metric_key, t)
) PARTITION BY RANGE (t);

CREATE INDEX metric_samples_lookup_idx
    ON metric_samples (tenant_id, native_id, metric_key, t DESC);

-- Seed a fortnight of daily partitions plus a catch-all. A maintenance job
-- extends this; the default partition means an out-of-range timestamp is stored
-- rather than rejected, which matters because providers backfill late data.
DO $$
DECLARE
    d date := current_date - 2;
    i int;
BEGIN
    FOR i IN 0..15 LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS metric_samples_%s PARTITION OF metric_samples
               FOR VALUES FROM (%L) TO (%L)',
            to_char(d + i, 'YYYY_MM_DD'), (d + i)::text, (d + i + 1)::text);
    END LOOP;
END $$;

CREATE TABLE metric_samples_default PARTITION OF metric_samples DEFAULT;

ALTER TABLE metric_samples ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON metric_samples
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON metric_samples TO nimbuseye_app;

-- ---------------------------------------------------------------------------
-- Default tenant
-- ---------------------------------------------------------------------------

INSERT INTO tenants (slug, display_name, timezone, currency, fy_start_month)
VALUES ('default', 'NimbusEye', 'Asia/Kolkata', 'INR', 1)
ON CONFLICT (slug) DO NOTHING;

-- Default profiles and business hours for that tenant, so a freshly discovered
-- resource has something to be evaluated against rather than nothing.
INSERT INTO business_hours (tenant_id, display_name, timezone, time_config)
SELECT t.id, 'Weekday 09:00-17:00', t.timezone,
       '[{"day":1,"start":"09:00","end":"17:00"},
         {"day":2,"start":"09:00","end":"17:00"},
         {"day":3,"start":"09:00","end":"17:00"},
         {"day":4,"start":"09:00","end":"17:00"},
         {"day":5,"start":"09:00","end":"17:00"}]'::jsonb
FROM tenants t WHERE t.slug = 'default'
ON CONFLICT (tenant_id, display_name) DO NOTHING;

INSERT INTO notification_profiles
    (tenant_id, display_name, notification_delay, notify_on_recovery, rca_needed, is_default)
SELECT t.id, 'Default Notification', 1, true, true, true
FROM tenants t WHERE t.slug = 'default'
ON CONFLICT (tenant_id, display_name) DO NOTHING;

-- One default threshold profile per resource type, seeded from the catalog's own
-- default trouble/critical values. This is what makes the thresholds a chart
-- draws and the thresholds an evaluator uses provably the same numbers.
INSERT INTO threshold_profiles
    (tenant_id, display_name, resource_type, rules, down_polls_check, system_generated, is_default)
SELECT
    t.id,
    'Default Threshold - ' || rt.display_name,
    rt.code,
    COALESCE(
        (SELECT jsonb_agg(jsonb_build_object(
            'metric',      m->>'key',
            'op',          CASE WHEN (m->>'higher_is_worse')::boolean THEN '>=' ELSE '<=' END,
            'trouble',     m->'trouble',
            'critical',    m->'critical',
            'polls_check', 3,
            'strategy',    'consecutive'))
         FROM jsonb_array_elements(rt.metric_defs) m
         WHERE m->'trouble' <> 'null'::jsonb OR m->'critical' <> 'null'::jsonb),
        '[]'::jsonb),
    2, true, true
FROM tenants t
CROSS JOIN resource_types rt
WHERE t.slug = 'default'
ON CONFLICT (tenant_id, display_name) DO NOTHING;

COMMIT;

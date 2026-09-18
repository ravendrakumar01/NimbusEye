-- NimbusEye — make default threshold profile names unique per resource type.
--
-- 006 named them 'Default Threshold - <display_name>', which collides wherever
-- two providers use the same service name. "Load Balancer" exists for OCI, Azure
-- and GCP, so `UNIQUE (tenant_id, display_name)` silently dropped two of the
-- three via ON CONFLICT DO NOTHING: 44 profiles were created where 46 were
-- expected, and the two affected resource types were left with no default
-- threshold at all.
--
-- That is the worst shape for this bug — no error, and monitors of those types
-- would simply never be evaluated. Including the provider in the name fixes it
-- and keeps the names readable.

BEGIN;

-- Rename what exists so the provider is visible.
UPDATE threshold_profiles tp
SET display_name = 'Default Threshold - ' || upper(rt.provider) || ' ' || rt.display_name
FROM resource_types rt
WHERE tp.resource_type = rt.code
  AND tp.system_generated
  AND tp.display_name = 'Default Threshold - ' || rt.display_name;

-- Insert the ones that were skipped.
INSERT INTO threshold_profiles
    (tenant_id, display_name, resource_type, rules, down_polls_check, system_generated, is_default)
SELECT
    t.id,
    'Default Threshold - ' || upper(rt.provider) || ' ' || rt.display_name,
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
  AND NOT EXISTS (
      SELECT 1 FROM threshold_profiles x
      WHERE x.tenant_id = t.id AND x.resource_type = rt.code
  );

-- Guard the invariant rather than trusting the insert: every resource type must
-- have exactly one default profile. If this ever fails, the migration aborts
-- instead of leaving types unevaluated.
DO $$
DECLARE
    missing int;
BEGIN
    SELECT count(*) INTO missing
    FROM resource_types rt
    WHERE NOT EXISTS (
        SELECT 1 FROM threshold_profiles tp
        JOIN tenants t ON t.id = tp.tenant_id AND t.slug = 'default'
        WHERE tp.resource_type = rt.code AND tp.is_default
    );
    IF missing > 0 THEN
        RAISE EXCEPTION '% resource types still have no default threshold profile', missing;
    END IF;
END $$;

COMMIT;

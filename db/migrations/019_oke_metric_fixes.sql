-- NimbusEye — correct two OKE metrics that reported nothing usable.
--
-- Both were added in migration 015 and both looked fine until the data arrived.
--
-- etcd_object_count read exactly 0.0 on all three clusters, across seventy samples
-- each. ETCDObjectCount carries a `resource` dimension naming the Kubernetes object
-- kind, and a query that does not supply it aggregates to nothing. A metric that
-- always reads zero is worse than an absent one: zero looks like a measurement, so
-- nobody questions it. Removed rather than left in place looking healthy.
--
-- api_server_requests is a cumulative counter. It read between 15.7 and 21.8 million
-- and rising, which as a displayed figure tells a reader nothing at all. Switched to
-- the rate statistic so it reports control-plane activity per interval, which is
-- what somebody looking at it wants to know.
--
-- Kept in step with internal/catalog/catalog.json.

BEGIN;

UPDATE resource_types SET metric_defs = (
    SELECT coalesce(jsonb_agg(
        CASE WHEN m->>'key' = 'api_server_requests'
             THEN m || '{"statistic": "rate", "label": "API Server Requests"}'::jsonb
             ELSE m END), '[]'::jsonb)
    FROM jsonb_array_elements(metric_defs) m
    WHERE m->>'key' <> 'etcd_object_count')
WHERE code = 'OCI_OKE_CLUSTER';

-- Any stored threshold rule for the removed metric would name a metric the type no
-- longer defines, which migration 015's guard rejects.
UPDATE threshold_profiles tp
SET rules = coalesce((
        SELECT jsonb_agg(r) FROM jsonb_array_elements(tp.rules) r
         WHERE r->>'metric' <> 'etcd_object_count'), '[]'::jsonb),
    updated_at = now()
WHERE tp.resource_type = 'OCI_OKE_CLUSTER'
  AND tp.rules @> '[{"metric": "etcd_object_count"}]'::jsonb;

-- The samples already collected are all zero and would flatten any chart drawn from
-- the remaining data.
DELETE FROM metric_samples WHERE metric_key = 'etcd_object_count';

COMMIT;

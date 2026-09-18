-- NimbusEye — correct the OCI metric definitions against what OCI actually
-- publishes.
--
-- Every metric name and namespace in this file was read back from the OCI
-- Monitoring ListMetrics API against a live tenancy. That check found twelve of
-- our twenty-nine OCI metric definitions naming metrics that do not exist, nine
-- of them carrying thresholds — so they appeared in the console as active
-- alerting rules and could never fire. On the tenancy in question that included:
--
--   OCI_LOAD_BALANCER.backend_health -> HealthyBackendServers
--       The single most important load balancer alert. The metric is called
--       UnHealthyBackendServers, which is also the better signal: higher-is-worse
--       means "a backend has gone" is a threshold, rather than a floor you need
--       the total backend count to interpret.
--
--   OCI_COMPUTE_INSTANCE.disk_utilization -> DiskUtilization
--       oci_computeagent does not publish this at all. Filesystem fullness needs
--       an in-guest metric OCI does not expose, so the rule is removed rather
--       than left looking active. Thirty-nine instances carried it.
--
--   OCI_OBJECT_STORAGE_BUCKET.server_errors -> ServerErrors   (not published)
--   OCI_AUTONOMOUS_DB.apply_lag             -> ApplyLag, actually PeerLag
--
-- Separately, three resource types had no metric definitions at all while OCI
-- publishes useful ones: File Storage (FileSystemUsage — how full it is), OKE
-- (UnschedulablePods — a workload that cannot be placed) and the throttling
-- counter on Block Volume.
--
-- OCI_VCN is deliberately left with no metrics. The oci_vcn namespace exists but
-- every metric in it is scoped to a VNIC and carries an instanceId dimension, so
-- those figures describe instances, not the virtual network. Attaching them to a
-- VCN would produce a number that looks meaningful and is not.
--
-- OCI_DB_SYSTEM and OCI_FUNCTION are left untouched: the tenancy has no resources
-- of either type, so ListMetrics returning nothing proves nothing about whether
-- those names are right.
--
-- Kept in step with internal/catalog/catalog.json, which the binaries embed. This
-- file was generated from it rather than typed, so the two cannot drift.

BEGIN;

UPDATE resource_types rt SET metric_defs = v.defs
FROM (VALUES
    ('OCI_COMPUTE_INSTANCE', '[{"key": "cpu_utilization", "label": "CPU Utilization", "unit": "percent", "provider_metric": "CpuUtilization", "namespace": "oci_computeagent", "statistic": "mean", "trouble": 75, "critical": 90, "higher_is_worse": true}, {"key": "memory_utilization", "label": "Memory Utilization", "unit": "percent", "provider_metric": "MemoryUtilization", "namespace": "oci_computeagent", "statistic": "mean", "trouble": 80, "critical": 92, "higher_is_worse": true}, {"key": "network_bytes_in", "label": "Network In", "unit": "bytes_per_sec", "provider_metric": "NetworksBytesIn", "namespace": "oci_computeagent", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "network_bytes_out", "label": "Network Out", "unit": "bytes_per_sec", "provider_metric": "NetworksBytesOut", "namespace": "oci_computeagent", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "load_average", "label": "Load Average", "unit": "count", "provider_metric": "LoadAverage", "namespace": "oci_computeagent", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "memory_stalls", "label": "Memory Allocation Stalls", "unit": "count", "provider_metric": "MemoryAllocationStalls", "namespace": "oci_computeagent", "statistic": "sum", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb),
    ('OCI_BLOCK_VOLUME', '[{"key": "read_throughput", "label": "Read Throughput", "unit": "bytes_per_sec", "provider_metric": "VolumeReadThroughput", "namespace": "oci_blockstore", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "write_throughput", "label": "Write Throughput", "unit": "bytes_per_sec", "provider_metric": "VolumeWriteThroughput", "namespace": "oci_blockstore", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "guaranteed_vpus", "label": "Guaranteed VPUs", "unit": "count", "provider_metric": "VolumeGuaranteedVPUsPerGB", "namespace": "oci_blockstore", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": false}, {"key": "throttled_ios", "label": "Throttled I/Os", "unit": "count", "provider_metric": "VolumeThrottledIOs", "namespace": "oci_blockstore", "statistic": "sum", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb),
    ('OCI_OBJECT_STORAGE_BUCKET', '[{"key": "bucket_size", "label": "Bucket Size", "unit": "bytes", "provider_metric": "StoredBytes", "namespace": "oci_objectstorage", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "object_count", "label": "Object Count", "unit": "count", "provider_metric": "ObjectCount", "namespace": "oci_objectstorage", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "all_requests", "label": "All Requests", "unit": "ops_per_sec", "provider_metric": "AllRequests", "namespace": "oci_objectstorage", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "client_errors", "label": "Client Errors (4xx)", "unit": "count", "provider_metric": "ClientErrors", "namespace": "oci_objectstorage", "statistic": "sum", "trouble": 10, "critical": 50, "higher_is_worse": true}, {"key": "request_latency", "label": "Total Request Latency", "unit": "milliseconds", "provider_metric": "TotalRequestLatency", "namespace": "oci_objectstorage", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "uncommitted_parts", "label": "Uncommitted Multipart Parts", "unit": "count", "provider_metric": "UncommittedParts", "namespace": "oci_objectstorage", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb),
    ('OCI_AUTONOMOUS_DB', '[{"key": "cpu_utilization", "label": "CPU Utilization", "unit": "percent", "provider_metric": "CpuUtilization", "namespace": "oci_autonomous_database", "statistic": "mean", "trouble": 75, "critical": 90, "higher_is_worse": true}, {"key": "storage_utilization", "label": "Storage Utilization", "unit": "percent", "provider_metric": "StorageUtilization", "namespace": "oci_autonomous_database", "statistic": "max", "trouble": 80, "critical": 90, "higher_is_worse": true}, {"key": "sessions", "label": "Sessions", "unit": "count", "provider_metric": "Sessions", "namespace": "oci_autonomous_database", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "execute_count", "label": "Execute Count", "unit": "ops_per_sec", "provider_metric": "ExecuteCount", "namespace": "oci_autonomous_database", "statistic": "rate", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "apply_lag", "label": "Peer Lag", "unit": "seconds", "provider_metric": "PeerLag", "namespace": "oci_autonomous_database", "statistic": "max", "trouble": 60, "critical": 300, "higher_is_worse": true}, {"key": "database_availability", "label": "Database Availability", "unit": "percent", "provider_metric": "DatabaseAvailability", "namespace": "oci_autonomous_database", "statistic": "mean", "trouble": 100, "critical": 99, "higher_is_worse": false}, {"key": "blocking_sessions", "label": "Blocking Sessions", "unit": "count", "provider_metric": "BlockingSessions", "namespace": "oci_autonomous_database", "statistic": "max", "trouble": 1, "critical": 5, "higher_is_worse": true}, {"key": "session_utilization", "label": "Session Utilisation", "unit": "percent", "provider_metric": "SessionUtilization", "namespace": "oci_autonomous_database", "statistic": "mean", "trouble": 80, "critical": 92, "higher_is_worse": true}]'::jsonb),
    ('OCI_LOAD_BALANCER', '[{"key": "active_connections", "label": "Active Connections", "unit": "count", "provider_metric": "ActiveConnections", "namespace": "oci_lbaas", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "unhealthy_backends", "label": "Unhealthy Backends", "unit": "count", "provider_metric": "UnHealthyBackendServers", "namespace": "oci_lbaas", "statistic": "max", "trouble": 1, "critical": 2, "higher_is_worse": true}, {"key": "response_time", "label": "Time To First Byte", "unit": "milliseconds", "provider_metric": "ResponseTimeFirstByte", "namespace": "oci_lbaas", "statistic": "mean", "trouble": 1000, "critical": 3000, "higher_is_worse": true}, {"key": "backend_timeouts", "label": "Backend Timeouts", "unit": "count", "provider_metric": "BackendTimeouts", "namespace": "oci_lbaas", "statistic": "sum", "trouble": 1, "critical": 10, "higher_is_worse": true}]'::jsonb),
    ('OCI_OKE_CLUSTER', '[{"key": "unschedulable_pods", "label": "Unschedulable Pods", "unit": "count", "provider_metric": "UnschedulablePods", "namespace": "oci_oke", "statistic": "max", "trouble": 1, "critical": 5, "higher_is_worse": true}, {"key": "api_server_requests", "label": "API Server Requests", "unit": "count", "provider_metric": "APIServerRequestCount", "namespace": "oci_oke", "statistic": "sum", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "etcd_object_count", "label": "etcd Objects", "unit": "count", "provider_metric": "ETCDObjectCount", "namespace": "oci_oke", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb),
    ('OCI_FILE_STORAGE', '[{"key": "filesystem_usage", "label": "File System Usage", "unit": "bytes", "provider_metric": "FileSystemUsage", "namespace": "oci_filestorage", "statistic": "max", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "mount_target_connections", "label": "Mount Target Connections", "unit": "count", "provider_metric": "MountTargetConnections", "namespace": "oci_filestorage", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "fs_read_throughput", "label": "Read Throughput", "unit": "bytes_per_sec", "provider_metric": "FileSystemReadThroughput", "namespace": "oci_filestorage", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}, {"key": "fs_write_throughput", "label": "Write Throughput", "unit": "bytes_per_sec", "provider_metric": "FileSystemWriteThroughput", "namespace": "oci_filestorage", "statistic": "mean", "trouble": null, "critical": null, "higher_is_worse": true}]'::jsonb)
) AS v(code, defs)
WHERE rt.code = v.code;

-- Rebuild the default threshold profiles for those types from the corrected
-- definitions. Without this the definitions are right and nothing evaluates them,
-- which is the quiet half-configured state that produced this bug in the first
-- place.
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
    '[]'::jsonb),
    updated_at = now()
FROM resource_types rt
WHERE tp.resource_type = rt.code
  AND tp.system_generated
  AND rt.code IN ('OCI_COMPUTE_INSTANCE', 'OCI_BLOCK_VOLUME', 'OCI_OBJECT_STORAGE_BUCKET', 'OCI_AUTONOMOUS_DB', 'OCI_LOAD_BALANCER', 'OCI_OKE_CLUSTER', 'OCI_FILE_STORAGE');

-- Guard: no stored rule may name a metric its type does not define. That is the
-- exact shape of the bug this migration fixes, so it is worth failing the
-- migration rather than trusting the statements above to have caught everything.
DO $$
DECLARE
    bad text;
BEGIN
    SELECT string_agg(DISTINCT tp.resource_type || '.' || (r->>'metric'), ', ')
      INTO bad
      FROM threshold_profiles tp
      JOIN resource_types rt ON rt.code = tp.resource_type
      CROSS JOIN LATERAL jsonb_array_elements(tp.rules) r
     WHERE NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(rt.metric_defs) m
         WHERE m->>'key' = r->>'metric');
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION
            'threshold rules reference metrics their resource type does not define: %', bad;
    END IF;
END $$;

COMMIT;

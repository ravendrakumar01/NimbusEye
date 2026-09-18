-- NimbusEye — resource type catalog
--
-- This is the single source of truth for "what can be monitored". Three
-- consumers read it, which is why it lives in the database rather than in code:
--   * the UI     -> renders the Add Monitor tree, icons and metric charts
--   * the alerter-> builds the threshold editor and validates rule metric keys
--   * the collector -> knows which provider metric to query and how to aggregate
--
-- Adding a new cloud service is one INSERT plus a collector mapping. No schema
-- change, no UI change, no redeploy of the alerter.
--
-- metric_defs entry shape:
--   key             internal metric name, used in threshold rules and VictoriaMetrics
--   label           what the user sees
--   unit            percent | bytes | count | seconds | milliseconds | bytes_per_sec | ops_per_sec
--   provider_metric the provider's own metric name
--   namespace       provider metric namespace/scope
--   statistic       mean | max | min | sum | rate
--   trouble/critical default threshold values, NULL where a default is meaningless
--   higher_is_worse false for metrics where a LOW value is the problem (e.g. free disk)

BEGIN;

-- ===========================================================================
-- ORACLE CLOUD INFRASTRUCTURE
-- Namespaces are OCI Monitoring namespaces; oci_computeagent requires the
-- compute agent plugin to be enabled on the instance, which is a common reason
-- for a discovered instance reporting no metrics at all.
-- ===========================================================================

INSERT INTO resource_types
 (code, provider, category, display_name, icon, supports_availability, supports_metrics,
  supports_cost, default_poll_sec, metric_defs) VALUES

('OCI_COMPUTE_INSTANCE','oci','compute','Compute Instance','server',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"CpuUtilization","namespace":"oci_computeagent","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"memory_utilization","label":"Memory Utilization","unit":"percent","provider_metric":"MemoryUtilization","namespace":"oci_computeagent","statistic":"mean","trouble":80,"critical":92,"higher_is_worse":true},
   {"key":"disk_utilization","label":"Disk Utilization","unit":"percent","provider_metric":"DiskUtilization","namespace":"oci_computeagent","statistic":"max","trouble":80,"critical":90,"higher_is_worse":true},
   {"key":"network_bytes_in","label":"Network In","unit":"bytes_per_sec","provider_metric":"NetworksBytesIn","namespace":"oci_computeagent","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"network_bytes_out","label":"Network Out","unit":"bytes_per_sec","provider_metric":"NetworksBytesOut","namespace":"oci_computeagent","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"load_average","label":"Load Average","unit":"count","provider_metric":"LoadAverage","namespace":"oci_computeagent","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('OCI_BLOCK_VOLUME','oci','storage','Block Volume','disk',true,true,true,300,
 '[{"key":"read_throughput","label":"Read Throughput","unit":"bytes_per_sec","provider_metric":"VolumeReadThroughput","namespace":"oci_blockstore","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"write_throughput","label":"Write Throughput","unit":"bytes_per_sec","provider_metric":"VolumeWriteThroughput","namespace":"oci_blockstore","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"guaranteed_vpus","label":"Guaranteed VPUs","unit":"count","provider_metric":"VolumeGuaranteedVPUsPerGB","namespace":"oci_blockstore","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":false}]'::jsonb),

-- The type the evaluated account actually had: STORAGE-BUCKET.
('OCI_OBJECT_STORAGE_BUCKET','oci','storage','Object Storage Bucket','bucket',true,true,true,900,
 '[{"key":"bucket_size","label":"Bucket Size","unit":"bytes","provider_metric":"StoredBytes","namespace":"oci_objectstorage","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"object_count","label":"Object Count","unit":"count","provider_metric":"ObjectCount","namespace":"oci_objectstorage","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"all_requests","label":"All Requests","unit":"ops_per_sec","provider_metric":"AllRequests","namespace":"oci_objectstorage","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"client_errors","label":"Client Errors (4xx)","unit":"count","provider_metric":"ClientErrors","namespace":"oci_objectstorage","statistic":"sum","trouble":10,"critical":50,"higher_is_worse":true},
   {"key":"server_errors","label":"Server Errors (5xx)","unit":"count","provider_metric":"ServerErrors","namespace":"oci_objectstorage","statistic":"sum","trouble":1,"critical":5,"higher_is_worse":true}]'::jsonb),

('OCI_AUTONOMOUS_DB','oci','database','Autonomous Database','database',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"CpuUtilization","namespace":"oci_autonomous_database","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"storage_utilization","label":"Storage Utilization","unit":"percent","provider_metric":"StorageUtilization","namespace":"oci_autonomous_database","statistic":"max","trouble":80,"critical":90,"higher_is_worse":true},
   {"key":"sessions","label":"Sessions","unit":"count","provider_metric":"Sessions","namespace":"oci_autonomous_database","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"execute_count","label":"Execute Count","unit":"ops_per_sec","provider_metric":"ExecuteCount","namespace":"oci_autonomous_database","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"apply_lag","label":"Apply Lag","unit":"seconds","provider_metric":"ApplyLag","namespace":"oci_autonomous_database","statistic":"max","trouble":60,"critical":300,"higher_is_worse":true}]'::jsonb),

('OCI_DB_SYSTEM','oci','database','DB System','database',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"CpuUtilization","namespace":"oci_database","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"memory_utilization","label":"Memory Utilization","unit":"percent","provider_metric":"MemoryUtilization","namespace":"oci_database","statistic":"mean","trouble":80,"critical":92,"higher_is_worse":true},
   {"key":"storage_used_pct","label":"Storage Used","unit":"percent","provider_metric":"StorageUsedByTableSpace","namespace":"oci_database","statistic":"max","trouble":80,"critical":90,"higher_is_worse":true}]'::jsonb),

('OCI_LOAD_BALANCER','oci','network','Load Balancer','balance',true,true,true,300,
 '[{"key":"active_connections","label":"Active Connections","unit":"count","provider_metric":"ActiveConnections","namespace":"oci_lbaas","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"backend_health","label":"Healthy Backends","unit":"count","provider_metric":"HealthyBackendServers","namespace":"oci_lbaas","statistic":"min","trouble":1,"critical":0,"higher_is_worse":false},
   {"key":"response_time","label":"Backend Response Time","unit":"milliseconds","provider_metric":"BackendServerResponseTime","namespace":"oci_lbaas","statistic":"mean","trouble":1000,"critical":3000,"higher_is_worse":true},
   {"key":"http_5xx","label":"HTTP 5xx","unit":"count","provider_metric":"HttpResponses5xx","namespace":"oci_lbaas","statistic":"sum","trouble":5,"critical":25,"higher_is_worse":true}]'::jsonb),

('OCI_OKE_CLUSTER','oci','container','OKE Cluster','kubernetes',true,true,true,300,'[]'::jsonb),
('OCI_FUNCTION','oci','serverless','Function','function',true,true,true,300,
 '[{"key":"invocations","label":"Invocations","unit":"count","provider_metric":"FunctionInvocationCount","namespace":"oci_faas","statistic":"sum","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"errors","label":"Errors","unit":"count","provider_metric":"FunctionErrorCount","namespace":"oci_faas","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true},
   {"key":"duration","label":"Duration","unit":"milliseconds","provider_metric":"FunctionExecutionDuration","namespace":"oci_faas","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('OCI_VCN','oci','network','Virtual Cloud Network','network',false,true,true,900,'[]'::jsonb),
('OCI_FILE_STORAGE','oci','storage','File Storage','folder',true,true,true,900,'[]'::jsonb),

-- ===========================================================================
-- AMAZON WEB SERVICES
-- ===========================================================================

('AWS_EC2_INSTANCE','aws','compute','EC2 Instance','server',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"CPUUtilization","namespace":"AWS/EC2","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"status_check_failed","label":"Status Check Failed","unit":"count","provider_metric":"StatusCheckFailed","namespace":"AWS/EC2","statistic":"max","trouble":1,"critical":1,"higher_is_worse":true},
   {"key":"network_in","label":"Network In","unit":"bytes_per_sec","provider_metric":"NetworkIn","namespace":"AWS/EC2","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"network_out","label":"Network Out","unit":"bytes_per_sec","provider_metric":"NetworkOut","namespace":"AWS/EC2","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"ebs_read_ops","label":"EBS Read Ops","unit":"ops_per_sec","provider_metric":"EBSReadOps","namespace":"AWS/EC2","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"memory_utilization","label":"Memory Utilization","unit":"percent","provider_metric":"mem_used_percent","namespace":"CWAgent","statistic":"mean","trouble":80,"critical":92,"higher_is_worse":true}]'::jsonb),

('AWS_RDS_INSTANCE','aws','database','RDS Instance','database',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"CPUUtilization","namespace":"AWS/RDS","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"free_storage_space","label":"Free Storage Space","unit":"bytes","provider_metric":"FreeStorageSpace","namespace":"AWS/RDS","statistic":"min","trouble":10737418240,"critical":2147483648,"higher_is_worse":false},
   {"key":"freeable_memory","label":"Freeable Memory","unit":"bytes","provider_metric":"FreeableMemory","namespace":"AWS/RDS","statistic":"min","trouble":536870912,"critical":134217728,"higher_is_worse":false},
   {"key":"db_connections","label":"DB Connections","unit":"count","provider_metric":"DatabaseConnections","namespace":"AWS/RDS","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"read_latency","label":"Read Latency","unit":"seconds","provider_metric":"ReadLatency","namespace":"AWS/RDS","statistic":"mean","trouble":0.02,"critical":0.1,"higher_is_worse":true},
   {"key":"replica_lag","label":"Replica Lag","unit":"seconds","provider_metric":"ReplicaLag","namespace":"AWS/RDS","statistic":"max","trouble":30,"critical":300,"higher_is_worse":true}]'::jsonb),

('AWS_S3_BUCKET','aws','storage','S3 Bucket','bucket',true,true,true,900,
 '[{"key":"bucket_size","label":"Bucket Size","unit":"bytes","provider_metric":"BucketSizeBytes","namespace":"AWS/S3","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"object_count","label":"Object Count","unit":"count","provider_metric":"NumberOfObjects","namespace":"AWS/S3","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"5xx_errors","label":"5xx Errors","unit":"count","provider_metric":"5xxErrors","namespace":"AWS/S3","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true}]'::jsonb),

('AWS_ALB','aws','network','Application Load Balancer','balance',true,true,true,300,
 '[{"key":"target_response_time","label":"Target Response Time","unit":"seconds","provider_metric":"TargetResponseTime","namespace":"AWS/ApplicationELB","statistic":"mean","trouble":1,"critical":3,"higher_is_worse":true},
   {"key":"healthy_hosts","label":"Healthy Hosts","unit":"count","provider_metric":"HealthyHostCount","namespace":"AWS/ApplicationELB","statistic":"min","trouble":1,"critical":0,"higher_is_worse":false},
   {"key":"http_5xx","label":"HTTP 5xx (Target)","unit":"count","provider_metric":"HTTPCode_Target_5XX_Count","namespace":"AWS/ApplicationELB","statistic":"sum","trouble":5,"critical":25,"higher_is_worse":true},
   {"key":"rejected_connections","label":"Rejected Connections","unit":"count","provider_metric":"RejectedConnectionCount","namespace":"AWS/ApplicationELB","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true}]'::jsonb),

('AWS_LAMBDA_FUNCTION','aws','serverless','Lambda Function','function',true,true,true,300,
 '[{"key":"invocations","label":"Invocations","unit":"count","provider_metric":"Invocations","namespace":"AWS/Lambda","statistic":"sum","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"errors","label":"Errors","unit":"count","provider_metric":"Errors","namespace":"AWS/Lambda","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true},
   {"key":"duration","label":"Duration","unit":"milliseconds","provider_metric":"Duration","namespace":"AWS/Lambda","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"throttles","label":"Throttles","unit":"count","provider_metric":"Throttles","namespace":"AWS/Lambda","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true},
   {"key":"concurrent_executions","label":"Concurrent Executions","unit":"count","provider_metric":"ConcurrentExecutions","namespace":"AWS/Lambda","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('AWS_EBS_VOLUME','aws','storage','EBS Volume','disk',true,true,true,300,
 '[{"key":"burst_balance","label":"Burst Balance","unit":"percent","provider_metric":"BurstBalance","namespace":"AWS/EBS","statistic":"min","trouble":50,"critical":20,"higher_is_worse":false},
   {"key":"queue_length","label":"Queue Length","unit":"count","provider_metric":"VolumeQueueLength","namespace":"AWS/EBS","statistic":"mean","trouble":10,"critical":32,"higher_is_worse":true}]'::jsonb),

('AWS_EKS_CLUSTER','aws','container','EKS Cluster','kubernetes',true,true,true,300,'[]'::jsonb),
('AWS_DYNAMODB_TABLE','aws','database','DynamoDB Table','database',true,true,true,300,
 '[{"key":"throttled_requests","label":"Throttled Requests","unit":"count","provider_metric":"ThrottledRequests","namespace":"AWS/DynamoDB","statistic":"sum","trouble":1,"critical":10,"higher_is_worse":true},
   {"key":"consumed_read_capacity","label":"Consumed Read Capacity","unit":"count","provider_metric":"ConsumedReadCapacityUnits","namespace":"AWS/DynamoDB","statistic":"sum","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),
('AWS_ELASTICACHE_NODE','aws','database','ElastiCache Node','database',true,true,true,300,'[]'::jsonb),
('AWS_NAT_GATEWAY','aws','network','NAT Gateway','network',true,true,true,300,'[]'::jsonb),

-- ===========================================================================
-- MICROSOFT AZURE
-- ===========================================================================

('AZURE_VM','azure','compute','Virtual Machine','server',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"Percentage CPU","namespace":"Microsoft.Compute/virtualMachines","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"available_memory","label":"Available Memory","unit":"bytes","provider_metric":"Available Memory Bytes","namespace":"Microsoft.Compute/virtualMachines","statistic":"min","trouble":536870912,"critical":134217728,"higher_is_worse":false},
   {"key":"disk_read_bytes","label":"Disk Read","unit":"bytes_per_sec","provider_metric":"Disk Read Bytes","namespace":"Microsoft.Compute/virtualMachines","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"network_in","label":"Network In","unit":"bytes_per_sec","provider_metric":"Network In Total","namespace":"Microsoft.Compute/virtualMachines","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('AZURE_SQL_DATABASE','azure','database','SQL Database','database',true,true,true,300,
 '[{"key":"dtu_consumption","label":"DTU Consumption","unit":"percent","provider_metric":"dtu_consumption_percent","namespace":"Microsoft.Sql/servers/databases","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"storage_percent","label":"Storage Used","unit":"percent","provider_metric":"storage_percent","namespace":"Microsoft.Sql/servers/databases","statistic":"max","trouble":80,"critical":90,"higher_is_worse":true},
   {"key":"deadlocks","label":"Deadlocks","unit":"count","provider_metric":"deadlock","namespace":"Microsoft.Sql/servers/databases","statistic":"sum","trouble":1,"critical":5,"higher_is_worse":true}]'::jsonb),

('AZURE_STORAGE_ACCOUNT','azure','storage','Storage Account','bucket',true,true,true,900,'[]'::jsonb),
('AZURE_APP_SERVICE','azure','compute','App Service','server',true,true,true,300,'[]'::jsonb),
('AZURE_AKS_CLUSTER','azure','container','AKS Cluster','kubernetes',true,true,true,300,'[]'::jsonb),
('AZURE_LOAD_BALANCER','azure','network','Load Balancer','balance',true,true,true,300,'[]'::jsonb),
('AZURE_FUNCTION_APP','azure','serverless','Function App','function',true,true,true,300,'[]'::jsonb),

-- ===========================================================================
-- GOOGLE CLOUD PLATFORM
-- ===========================================================================

('GCP_COMPUTE_INSTANCE','gcp','compute','Compute Engine Instance','server',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"compute.googleapis.com/instance/cpu/utilization","namespace":"gce_instance","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"network_received","label":"Network Received","unit":"bytes_per_sec","provider_metric":"compute.googleapis.com/instance/network/received_bytes_count","namespace":"gce_instance","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"disk_read_bytes","label":"Disk Read","unit":"bytes_per_sec","provider_metric":"compute.googleapis.com/instance/disk/read_bytes_count","namespace":"gce_instance","statistic":"rate","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('GCP_CLOUD_SQL','gcp','database','Cloud SQL Instance','database',true,true,true,300,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"cloudsql.googleapis.com/database/cpu/utilization","namespace":"cloudsql_database","statistic":"mean","trouble":75,"critical":90,"higher_is_worse":true},
   {"key":"disk_utilization","label":"Disk Utilization","unit":"percent","provider_metric":"cloudsql.googleapis.com/database/disk/utilization","namespace":"cloudsql_database","statistic":"max","trouble":80,"critical":90,"higher_is_worse":true},
   {"key":"active_connections","label":"Active Connections","unit":"count","provider_metric":"cloudsql.googleapis.com/database/postgresql/num_backends","namespace":"cloudsql_database","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true}]'::jsonb),

('GCP_GCS_BUCKET','gcp','storage','Cloud Storage Bucket','bucket',true,true,true,900,'[]'::jsonb),
('GCP_GKE_CLUSTER','gcp','container','GKE Cluster','kubernetes',true,true,true,300,'[]'::jsonb),
('GCP_CLOUD_FUNCTION','gcp','serverless','Cloud Function','function',true,true,true,300,'[]'::jsonb),
('GCP_LOAD_BALANCER','gcp','network','Load Balancer','balance',true,true,true,300,'[]'::jsonb),

-- ===========================================================================
-- KUBERNETES
-- Populated by an in-cluster agent pushing to the ingest endpoint, not by
-- polling a cloud API. Cost is not attributable per pod without a separate
-- allocation step, hence supports_cost = false on the pod-level types.
-- ===========================================================================

('K8S_CLUSTER','k8s','container','Kubernetes Cluster','kubernetes',true,true,false,60,
 '[{"key":"node_count","label":"Nodes Ready","unit":"count","provider_metric":"kube_node_status_condition","namespace":"kube-state-metrics","statistic":"min","trouble":null,"critical":null,"higher_is_worse":false},
   {"key":"pod_count","label":"Running Pods","unit":"count","provider_metric":"kube_pod_status_phase","namespace":"kube-state-metrics","statistic":"mean","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"cpu_requests_pct","label":"CPU Requests Committed","unit":"percent","provider_metric":"cluster:cpu_requests:ratio","namespace":"nimbuseye","statistic":"mean","trouble":85,"critical":95,"higher_is_worse":true}]'::jsonb),

('K8S_NODE','k8s','container','Kubernetes Node','server',true,true,false,60,
 '[{"key":"cpu_utilization","label":"CPU Utilization","unit":"percent","provider_metric":"node_cpu_seconds_total","namespace":"node-exporter","statistic":"rate","trouble":80,"critical":92,"higher_is_worse":true},
   {"key":"memory_utilization","label":"Memory Utilization","unit":"percent","provider_metric":"node_memory_MemAvailable_bytes","namespace":"node-exporter","statistic":"mean","trouble":80,"critical":92,"higher_is_worse":true},
   {"key":"disk_pressure","label":"Disk Pressure","unit":"count","provider_metric":"kube_node_status_condition","namespace":"kube-state-metrics","statistic":"max","trouble":1,"critical":1,"higher_is_worse":true},
   {"key":"pod_capacity_pct","label":"Pod Capacity Used","unit":"percent","provider_metric":"kube_node_status_allocatable_pods","namespace":"kube-state-metrics","statistic":"mean","trouble":85,"critical":95,"higher_is_worse":true}]'::jsonb),

('K8S_NAMESPACE','k8s','container','Kubernetes Namespace','folder',false,true,false,60,'[]'::jsonb),

('K8S_WORKLOAD','k8s','container','Kubernetes Workload','box',true,true,false,60,
 '[{"key":"replicas_unavailable","label":"Unavailable Replicas","unit":"count","provider_metric":"kube_deployment_status_replicas_unavailable","namespace":"kube-state-metrics","statistic":"max","trouble":1,"critical":1,"higher_is_worse":true},
   {"key":"restart_rate","label":"Container Restarts","unit":"count","provider_metric":"kube_pod_container_status_restarts_total","namespace":"kube-state-metrics","statistic":"rate","trouble":1,"critical":5,"higher_is_worse":true},
   {"key":"oom_killed","label":"OOM Killed","unit":"count","provider_metric":"kube_pod_container_status_last_terminated_reason","namespace":"kube-state-metrics","statistic":"sum","trouble":1,"critical":1,"higher_is_worse":true}]'::jsonb),

('K8S_POD','k8s','container','Kubernetes Pod','box',true,true,false,60,'[]'::jsonb),

-- ===========================================================================
-- SYNTHETIC / WEB
-- These are the only types NimbusEye actively probes rather than reads from a
-- provider API, so they are the only ones needing location profiles.
-- ===========================================================================

('WEB_HTTP','synthetic','web','Website',        'globe',true,true,false,60,
 '[{"key":"response_time","label":"Response Time","unit":"milliseconds","provider_metric":"http_duration_ms","namespace":"nimbuseye","statistic":"mean","trouble":2000,"critical":5000,"higher_is_worse":true},
   {"key":"status_code","label":"HTTP Status","unit":"count","provider_metric":"http_status","namespace":"nimbuseye","statistic":"max","trouble":null,"critical":null,"higher_is_worse":true},
   {"key":"dns_time","label":"DNS Lookup","unit":"milliseconds","provider_metric":"http_dns_ms","namespace":"nimbuseye","statistic":"mean","trouble":500,"critical":1500,"higher_is_worse":true},
   {"key":"tls_handshake","label":"TLS Handshake","unit":"milliseconds","provider_metric":"http_tls_ms","namespace":"nimbuseye","statistic":"mean","trouble":1000,"critical":3000,"higher_is_worse":true}]'::jsonb),

('WEB_REST_API','synthetic','web','REST API',   'api',  true,true,false,60,'[]'::jsonb),
('WEB_PORT','synthetic','web','Port / TCP',     'plug', true,true,false,60,'[]'::jsonb),
('WEB_PING','synthetic','web','PING',           'signal',true,true,false,60,'[]'::jsonb),
('WEB_DNS','synthetic','web','DNS Server',      'dns',  true,true,false,300,'[]'::jsonb),
('WEB_SSL_CERT','synthetic','web','SSL Certificate','lock',true,true,false,3600,
 '[{"key":"days_to_expiry","label":"Days to Expiry","unit":"count","provider_metric":"ssl_days_remaining","namespace":"nimbuseye","statistic":"min","trouble":30,"critical":7,"higher_is_worse":false}]'::jsonb),
('WEB_DOMAIN_EXPIRY','synthetic','web','Domain Expiry','calendar',true,false,false,86400,
 '[{"key":"days_to_expiry","label":"Days to Expiry","unit":"count","provider_metric":"domain_days_remaining","namespace":"nimbuseye","statistic":"min","trouble":30,"critical":7,"higher_is_worse":false}]'::jsonb),
('WEB_HEARTBEAT','synthetic','web','Heartbeat (inbound)','heart',true,false,false,60,'[]'::jsonb);

COMMIT;

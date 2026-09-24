package alert

// What to check, per metric.
//
// An alert that says a number is past a line leaves the reader to work out what to
// do with that. For the metrics that actually page somebody, one line naming the
// first thing to look at is worth more than any amount of chart.
//
// Deliberately kept next to the alerting code rather than in the catalog, and only
// for metrics that carry a threshold. A hint on a metric nobody is alerted about is
// documentation nobody reads, and would put 90 entries here to serve 20.
//
// These say where to look, not what the answer is. A hint that guesses at the cause
// would be wrong often enough to be worse than silence.
var metricHints = map[string]string{
	// Databases
	"storage_utilization": "Check what is consuming tablespace. On Autonomous Database " +
		"this is usually audit or log data with no retention policy, which grows until " +
		"somebody sets one. Storage can be scaled without downtime.",
	"cpu_utilization": "Look for a long-running or newly deployed query, a missing index, " +
		"or a batch job that has started overlapping with the working day.",
	"memory_utilization": "Check for a process that has grown rather than a sudden spike. " +
		"On a database, look at SGA and PGA sizing against what the instance actually has.",
	"session_utilization": "Sessions are near the configured limit. Look for an application " +
		"pool that is not returning connections, which shows up as many idle sessions " +
		"from one host.",
	"blocking_sessions": "One session is holding a lock others are waiting on. Identify the " +
		"blocker before killing anything: the waiters will clear on their own once it does.",
	"apply_lag": "The standby is behind. Check network throughput to the peer region and " +
		"whether redo generation has jumped on the primary.",
	"database_availability": "The database is not answering health checks. Check its " +
		"lifecycle state and any recent scaling or maintenance operation.",

	// Load balancers
	"unhealthy_backends": "The load balancer's health check is failing against specific " +
		"backends, listed above. Check the application on those hosts first, then the " +
		"health check path and the security rules between the load balancer subnet and " +
		"the backend subnet.",
	"backend_timeouts": "Backends are accepting connections and not answering in time. " +
		"Usually the application, not the network: check its own latency and thread pool " +
		"before looking at the load balancer.",
	"response_time": "Time to first byte is above the threshold. Compare against the " +
		"backends' own response time to see whether the delay is in the application or " +
		"in the path to it.",

	// Compute
	"load_average": "Load is above what the shape can serve. Compare against the vCPU " +
		"count before treating it as a problem: load 4 on four cores is busy, not broken.",
	"memory_stalls": "The kernel is stalling on memory allocation, which is a stronger " +
		"signal than utilisation alone. Expect this shortly before the OOM killer runs.",

	// Kubernetes
	"unschedulable_pods": "Pods cannot be placed. Usually no node has the requested CPU or " +
		"memory, a node selector matches nothing, or a persistent volume cannot attach in " +
		"the required availability domain.",

	// Object storage
	"client_errors": "4xx responses from the bucket. Usually a wrong key or an expired " +
		"pre-authenticated request rather than a fault; check which caller is generating them.",
	"uncommitted_parts": "Abandoned multipart uploads are being billed as stored data. " +
		"They are invisible in the object list. A lifecycle rule can delete them automatically.",

	// Synthetic checks
	"days_to_expiry": "The certificate or domain expires soon. Renewal usually needs to " +
		"happen well before the final day if DNS or a chain update is involved.",
	"dns_time": "Resolution is slow. Check the authoritative nameservers and whether " +
		"the record's TTL is forcing repeated lookups.",
	"tls_handshake": "The TLS handshake is slow. Look at the certificate chain length and " +
		"whether OCSP stapling is enabled.",
	"connect_time": "TCP connect is slow, which points at the network or a saturated " +
		"listen queue rather than the application.",
	"status_code": "The endpoint answered with an unexpected status. The body of the " +
		"response usually says more than the code does.",
}

// hintFor returns the guidance for a metric, or empty when there is none.
//
// Missing is the normal case for the many metrics that are charted and not alerted
// on, and an empty hint renders as nothing rather than as a placeholder.
func hintFor(metricKey string) string { return metricHints[metricKey] }

// downHint is used when there is no metric at all: the check itself failed.
const downHint = "The check did not complete. Confirm the target is reachable from " +
	"outside your network, then check DNS, the certificate if it is HTTPS, and any " +
	"security rule or firewall in the path."

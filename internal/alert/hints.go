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

// What the metric measures, in one sentence.
//
// Separate from the guidance because they answer different questions. A reader who
// has never seen "Time To First Byte 6270.8 ms" needs to know what was measured
// before any advice means anything — and an alert that assumes the reader already
// knows is an alert that gets forwarded to somebody else to interpret.
var metricMeanings = map[string]string{
	"response_time": "How long the backend took to return the first byte of a response, " +
		"measured at the load balancer. It covers the backend's own thinking time, not " +
		"the time to transfer the whole response.",
	"unhealthy_backends": "How many backends failed the load balancer's health check. " +
		"Traffic is not being sent to these, so the remaining backends are carrying all of it.",
	"backend_timeouts": "Requests the backend accepted and did not answer within the " +
		"load balancer's timeout.",
	"storage_utilization": "How full the database's allocated storage is. At 100% the " +
		"database stops accepting writes.",
	"cpu_utilization":    "Processor time in use, averaged over the collection interval.",
	"memory_utilization": "Memory in use as a share of what the instance has.",
	"session_utilization": "Open database sessions as a share of the configured maximum. " +
		"At the limit, new connections are refused.",
	"blocking_sessions": "Sessions holding a lock that other sessions are waiting on.",
	"database_availability": "Whether the database answered a health probe. 1 is " +
		"reachable, 0 is not.",
	"apply_lag": "How far behind the standby database is in applying redo from " +
		"the primary. This is the data you would lose in a failover.",
	"unschedulable_pods": "Pods that Kubernetes cannot place on any node, so the workload " +
		"they belong to is running below its requested capacity or not at all.",
	"load_average": "The run queue length, averaged. Compare it against the vCPU " +
		"count: equal to the core count means fully busy, not overloaded.",
	"memory_stalls": "Times the kernel had to pause a process while it reclaimed " +
		"memory. A stronger signal of pressure than utilisation.",
	"client_errors": "4xx responses the bucket returned, which are caller mistakes " +
		"rather than faults on the storage side.",
	"uncommitted_parts": "Bytes held by multipart uploads that were started and never " +
		"completed. They are billed as storage and do not appear in the object list.",
	"days_to_expiry":   "Days until the certificate or domain registration expires.",
	"dns_time":         "How long a DNS lookup for the target took.",
	"tls_handshake":    "How long the TLS handshake took, separate from the request itself.",
	"connect_time":     "How long the TCP connection took to establish.",
	"status_code":      "The HTTP status the endpoint returned.",
	"filesystem_usage": "Bytes stored in the file system.",
	"throttled_ios": "I/O requests the volume rejected because they exceeded its " +
		"provisioned performance.",
}

// meaningFor returns what a metric measures, or empty when nothing is recorded.
func meaningFor(metricKey string) string { return metricMeanings[metricKey] }

// hintFor returns the guidance for a metric, or empty when there is none.
//
// Missing is the normal case for the many metrics that are charted and not alerted
// on, and an empty hint renders as nothing rather than as a placeholder.
func hintFor(metricKey string) string { return metricHints[metricKey] }

// downHint is used when there is no metric at all: the check itself failed.
const downHint = "The check did not complete. Confirm the target is reachable from " +
	"outside your network, then check DNS, the certificate if it is HTTPS, and any " +
	"security rule or firewall in the path."

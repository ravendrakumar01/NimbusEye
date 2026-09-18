// Package collect orchestrates cloud discovery and metric collection.
//
// The collector is a separate process from the API on purpose. Cloud polling is
// long-running, rate-limited and occasionally wedged on a slow provider; keeping
// it out of the request path means a stuck OCI call cannot make the dashboard
// unresponsive. The two communicate over an ingest endpoint, which is also how
// they will be separated across hosts later.
package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	ociprov "nimbuseye/internal/providers/oci"
)

// Sink receives what a collection run produced.
//
// Two implementations exist: HTTPSink, which posts to the API, and the logging
// sink used by --dry-run. A PostgreSQL sink replaces HTTPSink once the database
// is provisioned; nothing else in this package changes when it does.
type Sink interface {
	PutResources(ctx context.Context, accountID string, resources []model.Resource) error
	PutMetrics(ctx context.Context, samples []Sample) error
	ReportRun(ctx context.Context, run RunReport) error
}

// Sample is one metric datapoint bound to a resource.
type Sample struct {
	NativeID  string    `json:"native_id"`
	MetricKey string    `json:"metric_key"`
	T         time.Time `json:"t"`
	V         float64   `json:"v"`
}

// RunReport is the outcome of one collection run, recorded so a silently failing
// integration is visible instead of merely producing no data.
type RunReport struct {
	AccountID     string    `json:"account_id"`
	Provider      string    `json:"provider"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	State         string    `json:"state"` // ok | partial | failed
	RegionsOK     []string  `json:"regions_ok"`
	RegionsFailed []string  `json:"regions_failed"`
	Discovered    int       `json:"discovered"`
	Mapped        int       `json:"mapped"`
	// Resource types OCI returned that the catalog does not model. Surfaced
	// rather than dropped: it is the difference between "nothing there" and
	// "we do not know how to look".
	UnmappedTypes map[string]int `json:"unmapped_types"`
	// Ignored counts types deliberately not monitored (identity objects,
	// backups, container images). Reported separately so the unmapped list stays
	// readable and a genuinely missing service type is visible in it.
	Ignored       int `json:"ignored"`
	MetricSamples int `json:"metric_samples"`
	// RateLimited counts metric reads lost to provider throttling even after
	// retrying. Reported rather than swallowed: a run that quietly collected half
	// the estate looks identical to one that collected all of it.
	RateLimited int `json:"rate_limited"`
	// Confirmed counts resources promoted from unknown to up because they
	// returned a metric this run. That is a real availability signal, unlike the
	// control plane merely reporting that a resource exists.
	Confirmed int `json:"confirmed_by_metrics"`
	// Retired counts resources found in a terminated state and therefore skipped
	// rather than stored as permanently unavailable.
	Retired int    `json:"retired"`
	Error   string `json:"error,omitempty"`
}

// OCIAccount is the configuration a run needs.
type OCIAccount struct {
	AccountID      string
	TenancyOCID    string
	UserOCID       string
	Fingerprint    string
	PrivateKeyPath string
	Regions        []string
	Compartments   []string
	// Metric window per run. Providers backfill late, so a window wider than the
	// poll interval is correct: it re-reads recent points rather than losing them.
	MetricWindow time.Duration
	// MetricsPerSecond paces metric reads; zero means the provider default.
	MetricsPerSecond float64
}

// RunOCI performs one discovery pass, then collects metrics for what it found.
func RunOCI(ctx context.Context, acct OCIAccount, sink Sink, log *slog.Logger, withMetrics bool) (RunReport, error) {
	report := RunReport{
		AccountID:     acct.AccountID,
		Provider:      "oci",
		StartedAt:     time.Now().UTC(),
		UnmappedTypes: map[string]int{},
	}
	if len(acct.Regions) == 0 {
		report.State = "failed"
		report.FinishedAt = time.Now().UTC()
		report.Error = "no regions configured"
		_ = sink.ReportRun(ctx, report)
		return report, fmt.Errorf("collect: %s", report.Error)
	}
	if acct.MetricWindow <= 0 {
		acct.MetricWindow = 30 * time.Minute
	}

	var all []model.Resource
	// compartmentOf lets the metric phase reuse what discovery already learned;
	// OCI metric reads are compartment-scoped and the OCID alone does not say
	// which compartment a resource is in.
	compartmentOf := map[string]string{}
	nameOf := map[string]string{}

	for _, region := range acct.Regions {
		client, err := ociprov.New(ociprov.Config{
			TenancyOCID:    acct.TenancyOCID,
			UserOCID:       acct.UserOCID,
			Fingerprint:    acct.Fingerprint,
			Region:         region,
			PrivateKeyPath: acct.PrivateKeyPath,
			Compartments:   acct.Compartments,
			// Paced below OCI's throttle for metric reads. Set from measurement,
			// not from a documented figure: at the previous unpaced rate a single
			// run lost 109 of its reads to 429s, and the only symptom was a debug
			// line indistinguishable from "this resource has no metrics".
			MetricsPerSecond: acct.MetricsPerSecond,
		})
		if err != nil {
			// A bad key or unreadable file fails every region identically, so
			// there is no point continuing.
			report.State = "failed"
			report.FinishedAt = time.Now().UTC()
			report.Error = err.Error()
			_ = sink.ReportRun(ctx, report)
			return report, err
		}

		if err := client.Ping(ctx); err != nil {
			log.Warn("region unreachable", "region", region, "err", err)
			report.RegionsFailed = append(report.RegionsFailed, region)
			continue
		}

		found, err := client.Discover(ctx, 1000)
		if err != nil {
			log.Warn("discovery failed", "region", region, "err", err)
			report.RegionsFailed = append(report.RegionsFailed, region)
			continue
		}
		report.Discovered += len(found)

		for _, d := range found {
			if ociprov.IsIgnored(d.OCIResourceType) {
				report.Ignored++
				continue
			}
			// Terminated resources are not monitored. Left in, they would sit in
			// every count forever as something permanently unavailable.
			if ociprov.IsGone(d) {
				report.Retired++
				continue
			}
			res, ok := ociprov.ToResource(d, "", acct.AccountID)
			if !ok {
				report.UnmappedTypes[d.OCIResourceType]++
				continue
			}
			all = append(all, res)
			compartmentOf[d.OCID] = d.CompartmentID
			nameOf[d.OCID] = d.DisplayName
		}
		report.RegionsOK = append(report.RegionsOK, region)
		log.Info("region discovered", "region", region, "resources", len(found))

		if withMetrics {
			n, reporting, err := collectOCIMetrics(ctx, client, all, compartmentOf, nameOf, acct.TenancyOCID, acct.MetricWindow, sink, log)
			if err != nil {
				log.Warn("metric collection incomplete", "region", region, "err", err)
			}
			report.MetricSamples += n
			// Throttled reads are data we asked for and did not get. Surfaced in
			// the report so a half-collected run is visibly different from a
			// complete one.
			report.RateLimited += int(client.RateLimited())
			// A resource that just returned a metric is demonstrably alive, which
			// is a real availability signal — unlike the control plane's opinion
			// that it exists. Anything AVAILABLE but silent stays unknown, which
			// usually means its monitoring plugin is not enabled, and that is worth
			// seeing rather than papering over.
			for i := range all {
				if all[i].Status == model.StatusUnknown && reporting[all[i].NativeID] {
					all[i].Status = model.StatusUp
					report.Confirmed++
				}
			}
		}
	}

	report.Mapped = len(all)
	report.FinishedAt = time.Now().UTC()

	switch {
	case len(report.RegionsOK) == 0:
		report.State = "failed"
		if report.Error == "" {
			report.Error = "no region could be reached"
		}
	case len(report.RegionsFailed) > 0:
		report.State = "partial"
	default:
		report.State = "ok"
	}

	if len(all) > 0 {
		if err := sink.PutResources(ctx, acct.AccountID, all); err != nil {
			report.State = "failed"
			report.Error = err.Error()
			_ = sink.ReportRun(ctx, report)
			return report, err
		}
	}
	if err := sink.ReportRun(ctx, report); err != nil {
		return report, err
	}
	if report.State == "failed" {
		return report, fmt.Errorf("collect: %s", report.Error)
	}
	return report, nil
}

// collectOCIMetrics fetches each catalog metric for each discovered resource.
//
// Fetched concurrently. OCI has no bulk metric read — every metric for every
// resource is its own MQL query — so a sequential loop is one round trip per
// metric. On a 146-resource tenancy that measured 2m15s of a 2m27s run; the
// discovery it followed took 11 seconds.
//
// The concurrency limit is deliberately modest. These calls are rate limited per
// tenancy, and overrunning that trades a slow collection for a throttled one,
// which is worse because the errors are indistinguishable from a real failure.
func collectOCIMetrics(
	ctx context.Context,
	client *ociprov.Client,
	resources []model.Resource,
	compartmentOf, nameOf map[string]string,
	tenancyOCID string,
	window time.Duration,
	sink Sink,
	log *slog.Logger,
) (int, map[string]bool, error) {
	to := time.Now().UTC()
	from := to.Add(-window)

	type query struct {
		resource    model.Resource
		metric      catalog.Metric
		dimValue    string
		compartment string
		subtree     bool
	}
	var queries []query
	noCompartment := 0
	for _, r := range resources {
		if r.Region != client.Region() {
			continue
		}
		t, ok := catalog.Get(r.ResourceType)
		if !ok || !t.SupportsMetrics {
			continue
		}
		// Resource Search does not report a compartment for every resource type —
		// buckets are one — and the previous code skipped those outright. That
		// silently excluded twenty-two buckets from metric collection with no log
		// line at all, because they never became a query to fail.
		//
		// Falling back to the tenancy root with a subtree search finds the metrics
		// wherever they were published, at the cost of a wider scan.
		compartment, subtree := compartmentOf[r.NativeID], false
		if compartment == "" {
			compartment, subtree = tenancyOCID, true
			noCompartment++
		}
		for _, m := range t.Metrics {
			if m.Namespace == "" || m.ProviderMetric == "" {
				continue
			}
			queries = append(queries, query{r, m, r.NativeID, compartment, subtree})
		}
	}
	if noCompartment > 0 {
		log.Debug("resources with no known compartment; querying from the tenancy root",
			"resources", noCompartment)
	}
	if len(queries) == 0 {
		return 0, map[string]bool{}, nil
	}

	const concurrency = 8
	var (
		mu        sync.Mutex
		samples   []Sample
		reporting = map[string]bool{}
		firstErr  error
		wg        sync.WaitGroup
	)
	sem := make(chan struct{}, concurrency)

	for _, q := range queries {
		wg.Add(1)
		go func(q query) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Each metric declares its own cadence where the provider publishes
			// slower than we poll. Using one window for everything meant hourly
			// metrics returned an empty series most runs — no error, just silence.
			mFrom, mRes := from, q.metric.Resolution()
			if w := q.metric.Window(to.Sub(from)); w > to.Sub(from) {
				mFrom = to.Add(-w)
			}

			pts, err := client.Metrics(ctx, ociprov.MetricQuery{
				Namespace:     q.metric.Namespace,
				MetricName:    q.metric.ProviderMetric,
				Statistic:     q.metric.Statistic,
				CompartmentID: q.compartment,
				Subtree:       q.subtree,
				ResourceOCID:  q.dimValue,
				Resolution:    mRes,
			}, mFrom, to)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// One metric failing is normal: the compute agent plugin may be
				// disabled, or the namespace may not apply to this shape. Logged at
				// debug rather than failing the run.
				log.Debug("metric unavailable",
					"resource", q.resource.DisplayName, "metric", q.metric.Key, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if len(pts) > 0 {
				reporting[q.resource.NativeID] = true
			}
			for _, p := range pts {
				samples = append(samples, Sample{
					NativeID: q.resource.NativeID, MetricKey: q.metric.Key, T: p.T, V: p.V,
				})
			}
		}(q)
	}
	wg.Wait()

	if len(samples) > 0 {
		if err := sink.PutMetrics(ctx, samples); err != nil {
			return 0, reporting, err
		}
	}
	return len(samples), reporting, firstErr
}

/* ------------------------------------------------------------------ sinks */

// HTTPSink posts results to the NimbusEye API.
type HTTPSink struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPSink builds a sink with sane timeouts.
func NewHTTPSink(baseURL, token string) *HTTPSink {
	return &HTTPSink{
		BaseURL: baseURL,
		Token:   token,
		Client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (s *HTTPSink) post(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// The ingest endpoints are not part of the user-facing API and are guarded by
	// a shared token, so a collector cannot be impersonated by a browser session.
	req.Header.Set("X-NimbusEye-Ingest-Token", s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ingest %s returned HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func (s *HTTPSink) PutResources(ctx context.Context, accountID string, resources []model.Resource) error {
	return s.post(ctx, "/api/v1/ingest/resources", map[string]any{
		"account_id": accountID,
		"resources":  resources,
	})
}

func (s *HTTPSink) PutMetrics(ctx context.Context, samples []Sample) error {
	// Chunked so a large tenancy does not produce a single enormous request.
	const chunk = 5000
	for i := 0; i < len(samples); i += chunk {
		end := min(i+chunk, len(samples))
		if err := s.post(ctx, "/api/v1/ingest/metrics", map[string]any{
			"samples": samples[i:end],
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *HTTPSink) ReportRun(ctx context.Context, run RunReport) error {
	return s.post(ctx, "/api/v1/ingest/run", run)
}

// LogSink writes what it would have sent, for --dry-run.
type LogSink struct{ Log *slog.Logger }

func (l LogSink) PutResources(_ context.Context, accountID string, resources []model.Resource) error {
	byType := map[string]int{}
	for _, r := range resources {
		byType[r.ResourceType]++
	}
	l.Log.Info("would store resources", "account", accountID, "count", len(resources), "by_type", byType)
	return nil
}

func (l LogSink) PutMetrics(_ context.Context, samples []Sample) error {
	l.Log.Info("would store metric samples", "count", len(samples))
	return nil
}

func (l LogSink) ReportRun(_ context.Context, run RunReport) error {
	l.Log.Info("run report",
		"state", run.State, "discovered", run.Discovered, "mapped", run.Mapped,
		"regions_ok", run.RegionsOK, "regions_failed", run.RegionsFailed,
		"unmapped_types", run.UnmappedTypes, "samples", run.MetricSamples,
		"error", run.Error)
	return nil
}

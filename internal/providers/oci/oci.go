// Package oci implements discovery and metric collection against Oracle Cloud
// Infrastructure.
//
// Design notes:
//
//   - Credentials are read from a file at collection time, never from the
//     database and never held longer than the client needs. The account record
//     holds only a path.
//   - Discovery uses the Resource Search service rather than per-service List
//     calls. One query returns every resource in the tenancy, which turns
//     discovery from ~40 API calls per compartment into a handful of paged calls.
//     This matters: per-service enumeration is what makes cloud discovery slow
//     enough to look broken.
//   - All operations are read-only. The client is constructed with credentials
//     that should carry only `read` verbs; nothing here issues a mutating call.
package oci

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/oracle/oci-go-sdk/v65/monitoring"
	"github.com/oracle/oci-go-sdk/v65/resourcesearch"
)

// Config is everything needed to talk to one tenancy in one region.
//
// PrivateKeyPath is a path, not key material: see the package comment.
type Config struct {
	TenancyOCID    string
	UserOCID       string
	Fingerprint    string
	Region         string
	PrivateKeyPath string
	// Optional. Empty means discover recursively from the tenancy root.
	Compartments []string
}

// Client bundles the OCI service clients for one tenancy/region pair.
type Client struct {
	cfg      Config
	search   resourcesearch.ResourceSearchClient
	monitor  monitoring.MonitoringClient
	identity identity.IdentityClient
}

// LoadPrivateKey reads the API signing key, refusing a world-readable file.
//
// The permission check is not pedantry: a private key readable by any local
// account is equivalent to handing over read access to the whole tenancy. Group
// access is allowed because the deployment pattern is root-owned and readable by
// the dedicated service group, which is stricter than the service owning it.
func LoadPrivateKey(path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("private key path must be absolute")
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("private key not found at %s", path)
		}
		return "", fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("private key %s is world-accessible (mode %o); run chmod 640", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	key := string(b)
	if !strings.Contains(key, "PRIVATE KEY") {
		return "", fmt.Errorf("%s does not look like a PEM private key", path)
	}
	return key, nil
}

// New builds a client for one region.
func New(cfg Config) (*Client, error) {
	for name, v := range map[string]string{
		"tenancy OCID":     cfg.TenancyOCID,
		"user OCID":        cfg.UserOCID,
		"fingerprint":      cfg.Fingerprint,
		"region":           cfg.Region,
		"private key path": cfg.PrivateKeyPath,
	} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("oci: %s is required", name)
		}
	}

	key, err := LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	provider := common.NewRawConfigurationProvider(
		cfg.TenancyOCID, cfg.UserOCID, cfg.Region, cfg.Fingerprint, key, nil)

	search, err := resourcesearch.NewResourceSearchClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("oci: resource search client: %w", err)
	}
	mon, err := monitoring.NewMonitoringClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("oci: monitoring client: %w", err)
	}
	idn, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("oci: identity client: %w", err)
	}

	return &Client{cfg: cfg, search: search, monitor: mon, identity: idn}, nil
}

// Region reports which region this client talks to.
func (c *Client) Region() string { return c.cfg.Region }

// Ping performs the cheapest authenticated call available, to prove the
// credentials and clock skew are acceptable before a full discovery run.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.identity.GetTenancy(ctx, identity.GetTenancyRequest{
		TenancyId: &c.cfg.TenancyOCID,
	})
	if err != nil {
		return fmt.Errorf("oci: authentication check failed: %w", err)
	}
	return nil
}

// Compartments lists the tenancy's active compartments, including the root.
func (c *Client) Compartments(ctx context.Context) ([]identity.Compartment, error) {
	var out []identity.Compartment
	var page *string
	subtree := true
	for {
		resp, err := c.identity.ListCompartments(ctx, identity.ListCompartmentsRequest{
			CompartmentId:          &c.cfg.TenancyOCID,
			CompartmentIdInSubtree: &subtree,
			AccessLevel:            identity.ListCompartmentsAccessLevelAccessible,
			LifecycleState:         identity.CompartmentLifecycleStateActive,
			Page:                   page,
		})
		if err != nil {
			return nil, fmt.Errorf("oci: list compartments: %w", err)
		}
		out = append(out, resp.Items...)
		if resp.OpcNextPage == nil {
			break
		}
		page = resp.OpcNextPage
	}
	return out, nil
}

// DiscoveredResource is one resource as OCI reports it, before mapping into the
// NimbusEye catalog.
type DiscoveredResource struct {
	OCID               string
	OCIResourceType    string
	DisplayName        string
	CompartmentID      string
	Region             string
	AvailabilityDomain string
	LifecycleState     string
	TimeCreated        *time.Time
	FreeformTags       map[string]string
	DefinedTags        map[string]map[string]any
}

// Discover returns every searchable resource the credentials can see.
//
// The query is deliberately `query all resources` rather than a per-type loop.
// Resource Search is the only OCI API that answers "what exists" in one place;
// anything else means N calls per compartment per service and a discovery run
// that takes long enough for users to assume it has hung.
func (c *Client) Discover(ctx context.Context, limit int) ([]DiscoveredResource, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	query := "query all resources"
	if len(c.cfg.Compartments) > 0 {
		// Structured search supports scoping by compartment id.
		quoted := make([]string, 0, len(c.cfg.Compartments))
		for _, id := range c.cfg.Compartments {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			quoted = append(quoted, "'"+id+"'")
		}
		if len(quoted) > 0 {
			query = fmt.Sprintf("query all resources where compartmentId = %s",
				strings.Join(quoted, " || compartmentId = "))
		}
	}

	var out []DiscoveredResource
	var page *string
	lim := limit
	for {
		details := resourcesearch.StructuredSearchDetails{Query: &query}
		resp, err := c.search.SearchResources(ctx, resourcesearch.SearchResourcesRequest{
			SearchDetails: details,
			Limit:         &lim,
			Page:          page,
		})
		if err != nil {
			return nil, fmt.Errorf("oci: search resources in %s: %w", c.cfg.Region, err)
		}
		for _, item := range resp.Items {
			d := DiscoveredResource{Region: c.cfg.Region}
			if item.Identifier != nil {
				d.OCID = *item.Identifier
			}
			if item.ResourceType != nil {
				d.OCIResourceType = *item.ResourceType
			}
			if item.DisplayName != nil {
				d.DisplayName = *item.DisplayName
			}
			if item.CompartmentId != nil {
				d.CompartmentID = *item.CompartmentId
			}
			if item.AvailabilityDomain != nil {
				d.AvailabilityDomain = *item.AvailabilityDomain
			}
			if item.LifecycleState != nil {
				d.LifecycleState = *item.LifecycleState
			}
			if item.TimeCreated != nil {
				t := item.TimeCreated.Time
				d.TimeCreated = &t
			}
			d.FreeformTags = item.FreeformTags
			d.DefinedTags = item.DefinedTags
			if d.OCID != "" {
				out = append(out, d)
			}
		}
		if resp.OpcNextPage == nil {
			break
		}
		page = resp.OpcNextPage
	}
	return out, nil
}

// MetricPoint is one sample.
type MetricPoint struct {
	T time.Time
	V float64
}

// MetricQuery describes one metric to fetch for one resource.
type MetricQuery struct {
	// OCI monitoring namespace, e.g. oci_computeagent.
	Namespace string
	// OCI metric name, e.g. CpuUtilization.
	MetricName string
	// mean | max | min | sum | rate
	Statistic string
	// Compartment the resource lives in; monitoring is compartment-scoped.
	CompartmentID string
	// Resource OCID, used as the resourceId dimension.
	ResourceOCID string
	// Aggregation window, e.g. 5m.
	Resolution string
}

// mql renders the Monitoring Query Language expression.
//
// OCI has no generic "give me metric X for resource Y" call; every read is an MQL
// expression, and the dimension key differs by namespace. Getting that key wrong
// returns an empty series rather than an error, which is why the mapping is
// explicit here instead of assumed.
func (q MetricQuery) mql() string {
	stat := q.Statistic
	switch stat {
	case "", "mean":
		stat = "mean"
	case "rate":
		// OCI expresses per-second rates as a rate() wrapper on a sum.
		return fmt.Sprintf("%s[%s]{%s = \"%s\"}.rate()",
			q.MetricName, q.Resolution, dimensionKey(q.Namespace), q.ResourceOCID)
	}
	return fmt.Sprintf("%s[%s]{%s = \"%s\"}.%s()",
		q.MetricName, q.Resolution, dimensionKey(q.Namespace), q.ResourceOCID, stat)
}

// dimensionKey returns the dimension that identifies a resource in a namespace.
func dimensionKey(namespace string) string {
	switch namespace {
	case "oci_objectstorage":
		// Object Storage keys on bucket name, not OCID; callers set ResourceOCID
		// to the bucket name for this namespace.
		return "bucketName"
	case "oci_lbaas":
		return "lbHostName"
	case "oci_autonomous_database", "oci_database":
		return "resourceId"
	default:
		return "resourceId"
	}
}

// Metrics fetches one metric series.
func (c *Client) Metrics(ctx context.Context, q MetricQuery, from, to time.Time) ([]MetricPoint, error) {
	if q.Resolution == "" {
		q.Resolution = "5m"
	}
	query := q.mql()
	start := common.SDKTime{Time: from.UTC()}
	end := common.SDKTime{Time: to.UTC()}

	resp, err := c.monitor.SummarizeMetricsData(ctx, monitoring.SummarizeMetricsDataRequest{
		CompartmentId: &q.CompartmentID,
		SummarizeMetricsDataDetails: monitoring.SummarizeMetricsDataDetails{
			Namespace: &q.Namespace,
			Query:     &query,
			StartTime: &start,
			EndTime:   &end,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("oci: summarize %s/%s: %w", q.Namespace, q.MetricName, err)
	}

	var out []MetricPoint
	for _, series := range resp.Items {
		for _, dp := range series.AggregatedDatapoints {
			if dp.Timestamp == nil || dp.Value == nil {
				continue
			}
			out = append(out, MetricPoint{T: dp.Timestamp.Time, V: *dp.Value})
		}
	}
	return out, nil
}

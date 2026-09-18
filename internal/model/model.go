// Package model holds the domain types shared by the API, the collectors and the
// alerter. Field names and JSON tags mirror the database columns in
// db/migrations/001_core.sql so there is one vocabulary across the whole system.
package model

import "time"

// Status values a resource can hold. Ordered by operational severity, which the
// UI relies on when rolling a group up to its worst member.
const (
	StatusDown        = "down"
	StatusCritical    = "critical"
	StatusTrouble     = "trouble"
	StatusUp          = "up"
	StatusMaintenance = "maintenance"
	StatusSuspended   = "suspended"
	StatusDiscovery   = "discovery"
	StatusUnknown     = "unknown"
)

// StatusRank orders statuses worst-first. Used for sorting and for group rollup.
var StatusRank = map[string]int{
	StatusDown: 0, StatusCritical: 1, StatusTrouble: 2, StatusUnknown: 3,
	StatusDiscovery: 4, StatusMaintenance: 5, StatusSuspended: 6, StatusUp: 7,
}

// IsUnhealthy reports whether a status should draw attention on the dashboard.
func IsUnhealthy(s string) bool {
	return s == StatusDown || s == StatusCritical || s == StatusTrouble
}

// Severity values for alarms.
const (
	SeverityDown     = "down"
	SeverityCritical = "critical"
	SeverityTrouble  = "trouble"
	SeverityInfo     = "info"
)

// Alarm lifecycle states.
const (
	AlarmOpen         = "open"
	AlarmAcknowledged = "acknowledged"
	AlarmResolved     = "resolved"
	AlarmSuppressed   = "suppressed"
)

// CloudAccount is a connected cloud tenancy, subscription or project.
type CloudAccount struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	DisplayName     string     `json:"display_name"`
	NativeAccountID string     `json:"native_account_id"`
	Regions         []string   `json:"regions"`
	Enabled         bool       `json:"enabled"`
	ResourceCount   int        `json:"resource_count"`
	LastDiscoveryAt *time.Time `json:"last_discovery_at"`
	// DiscoveryState surfaces collector health. A cloud integration that has
	// quietly stopped discovering is the failure mode that makes a monitoring
	// tool actively misleading, so it is part of the account payload rather
	// than buried in a log.
	DiscoveryState string `json:"discovery_state"`
	LastError      string `json:"last_error,omitempty"`

	// CredentialsRef is a path on the server, never the secret itself. It is
	// returned to the UI so an operator can confirm which file is in use;
	// the file's contents are never read by the API.
	CredentialsRef string `json:"credentials_ref,omitempty"`
	// Non-secret provider configuration: tenancy/user OCIDs, role ARN, tenant id.
	// These identify a principal but cannot authenticate as one.
	Config map[string]string `json:"config,omitempty"`

	DiscoveryIntervalSec int `json:"discovery_interval_sec,omitempty"`
	MetricIntervalSec    int `json:"metric_interval_sec,omitempty"`
}

// Resource is one monitored thing: the equivalent of a "monitor".
type Resource struct {
	ID               string            `json:"id"`
	CloudAccountID   string            `json:"cloud_account_id"`
	Provider         string            `json:"provider"`
	ResourceType     string            `json:"resource_type"`
	TypeName         string            `json:"type_name"`
	Category         string            `json:"category"`
	NativeID         string            `json:"native_id"`
	DisplayName      string            `json:"display_name"`
	Region           string            `json:"region"`
	AvailabilityZone string            `json:"availability_zone,omitempty"`
	Status           string            `json:"status"`
	StatusSince      time.Time         `json:"status_since"`
	LastPolledAt     *time.Time        `json:"last_polled_at"`
	Suspended        bool              `json:"suspended"`
	Tags             map[string]string `json:"tags"`
	GroupIDs         []string          `json:"group_ids"`
	Attributes       map[string]any    `json:"attributes"`
	// Availability over the trailing 24 hours, as a percentage.
	Availability24h float64 `json:"availability_24h"`
	OpenAlarms      int     `json:"open_alarms"`

	// Discovery lifecycle. DeletedAt is a soft delete: a resource that discovery
	// stops seeing keeps its availability history and cost attribution.
	DiscoveredAt time.Time `json:"discovered_at,omitempty"`
	LastSeenAt   time.Time `json:"last_seen_at,omitempty"`
	// ConfirmedAt is when a collected metric last proved this resource alive. Used
	// to hold an "up" status through gaps in cloud metric availability.
	ConfirmedAt time.Time  `json:"confirmed_at,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// ResourceGroup is a user-defined or rule-matched collection of resources.
type ResourceGroup struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description,omitempty"`
	ParentGroupID string `json:"parent_group_id,omitempty"`
	ResourceCount int    `json:"resource_count"`
	Status        string `json:"status"`
	Unhealthy     int    `json:"unhealthy"`
}

// Alarm is one firing alert condition on one resource.
type Alarm struct {
	ID             string     `json:"id"`
	ResourceID     string     `json:"resource_id"`
	ResourceName   string     `json:"resource_name"`
	ResourceType   string     `json:"resource_type"`
	Provider       string     `json:"provider"`
	Region         string     `json:"region"`
	DedupKey       string     `json:"dedup_key"`
	Severity       string     `json:"severity"`
	State          string     `json:"state"`
	MetricKey      string     `json:"metric_key,omitempty"`
	MetricLabel    string     `json:"metric_label,omitempty"`
	Unit           string     `json:"unit,omitempty"`
	ObservedValue  *float64   `json:"observed_value,omitempty"`
	ThresholdValue *float64   `json:"threshold_value,omitempty"`
	Message        string     `json:"message"`
	PollCount      int        `json:"poll_count"`
	OpenedAt       time.Time  `json:"opened_at"`
	AckedAt        *time.Time `json:"acknowledged_at,omitempty"`
	AckedBy        string     `json:"acknowledged_by,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	EscalationLvl  int        `json:"escalation_level"`
}

// DurationSec is how long the alarm has been open, in seconds.
func (a Alarm) DurationSec() int {
	end := time.Now()
	if a.ResolvedAt != nil {
		end = *a.ResolvedAt
	}
	return int(end.Sub(a.OpenedAt).Seconds())
}

// Outage is a closed or ongoing availability interval, used by reports.
type Outage struct {
	ID           string     `json:"id"`
	ResourceID   string     `json:"resource_id"`
	ResourceName string     `json:"resource_name"`
	Provider     string     `json:"provider"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
	DurationSec  int        `json:"duration_sec"`
	Severity     string     `json:"severity"`
	ClassifiedAs string     `json:"classified_as"`
	RootCause    string     `json:"root_cause,omitempty"`
	Comment      string     `json:"comment,omitempty"`
}

// StatusSummary powers the Home dashboard tiles. It is deliberately a single
// payload: rendering 2000 resources must not require the browser to compute
// aggregates or issue one request per tile.
type StatusSummary struct {
	Total          int                       `json:"total"`
	ByStatus       map[string]int            `json:"by_status"`
	ByProvider     map[string]map[string]int `json:"by_provider"`
	ByCategory     map[string]map[string]int `json:"by_category"`
	OpenAlarms     map[string]int            `json:"open_alarms"`
	Unacked        int                       `json:"unacked_alarms"`
	Availability   float64                   `json:"availability_24h"`
	OngoingOutages int                       `json:"ongoing_outages"`
	GeneratedAt    time.Time                 `json:"generated_at"`
}

// Sample is one point in a metric series.
type Sample struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// MetricSeries is a metric's history for one resource, plus the thresholds that
// apply, so a chart can draw its warning lines without a second request.
type MetricSeries struct {
	ResourceID string   `json:"resource_id"`
	MetricKey  string   `json:"metric_key"`
	Label      string   `json:"label"`
	Unit       string   `json:"unit"`
	Trouble    *float64 `json:"trouble,omitempty"`
	Critical   *float64 `json:"critical,omitempty"`
	Samples    []Sample `json:"samples"`
}

// ResourceCounts summarises the filtered result set, not the whole estate. The
// status rings on a filtered page must agree with the rows below them, so these
// are computed over the same predicate as the list itself.
type ResourceCounts struct {
	ByStatus map[string]int `json:"by_status"`
	// Counters shown beside the rings, mirroring how the reference console
	// separates operational state from configuration state.
	Total        int `json:"total"`
	Maintenance  int `json:"maintenance"`
	Discovery    int `json:"discovery"`
	Suspended    int `json:"suspended"`
	ConfigErrors int `json:"config_errors"`
	OpenAlarms   int `json:"open_alarms"`
	Anomalies    int `json:"anomalies"`
	// Mean 24h availability across the filtered set. Resources with no
	// availability history (suspended, still discovering) are excluded rather
	// than counted as zero, which would drag the figure down misleadingly.
	Availability float64 `json:"availability_24h"`
}

// ResourceList is Page[Resource] plus the aggregate counters the page header needs.
type ResourceList struct {
	Items    []Resource     `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Counts   ResourceCounts `json:"counts"`
}

// Page is the envelope for every list endpoint.
type Page[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

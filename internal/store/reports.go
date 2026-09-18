package store

import (
	"context"
	"time"
)

// Reporting types and contract.
//
// Reports are a separate interface from Store, implemented only by the PostgreSQL
// backend. The in-memory backend deliberately does not implement it: generating
// plausible report data would mean inventing availability history, and a report is
// exactly the artefact people forward to other people. A page that says "reports
// need the database" is better than one that quietly shows fiction.
//
// Every figure here is derived from availability_daily, outages and metric_samples
// — the same tables the dashboard reads — so a report and the live view cannot
// disagree.

// Range is the reporting window. Inclusive of both days.
type Range struct {
	From time.Time
	To   time.Time
}

// Days returns the number of days the range spans, at least one.
func (r Range) Days() int {
	d := int(r.To.Sub(r.From).Hours()/24) + 1
	if d < 1 {
		return 1
	}
	return d
}

// DailyPoint is one day of a resource's availability, for a trend line.
type DailyPoint struct {
	Day             string   `json:"day"`
	AvailabilityPct *float64 `json:"availability_pct"`
	DownSec         int      `json:"down_sec"`
}

// AvailabilityRow is one row of the availability report.
type AvailabilityRow struct {
	ResourceID   string `json:"resource_id"`
	DisplayName  string `json:"display_name"`
	ResourceType string `json:"resource_type"`
	TypeName     string `json:"type_name"`
	Provider     string `json:"provider"`
	Region       string `json:"region"`

	UpSec          int `json:"up_sec"`
	DownSec        int `json:"down_sec"`
	MaintenanceSec int `json:"maintenance_sec"`
	// AvailabilityPct is the mean of the daily figures, not up/(up+down) over the
	// whole window: a resource monitored for two of seven days should not be
	// reported as if the other five were perfect.
	AvailabilityPct *float64     `json:"availability_pct"`
	OutageCount     int          `json:"outage_count"`
	MTTRSec         *int         `json:"mttr_sec"`
	DaysWithData    int          `json:"days_with_data"`
	Daily           []DailyPoint `json:"daily,omitempty"`
}

// AvailabilitySummary is the header of the availability report.
type AvailabilitySummary struct {
	Range            Range    `json:"-"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	Resources        int      `json:"resources"`
	MeanAvailability *float64 `json:"mean_availability_pct"`
	TotalDownSec     int      `json:"total_down_sec"`
	TotalOutages     int      `json:"total_outages"`
	// ByProvider and ByType let the UI show a breakdown without a second query.
	ByProvider map[string]float64 `json:"by_provider"`
	ByType     map[string]float64 `json:"by_type"`
	Rows       []AvailabilityRow  `json:"rows"`
}

// OutageRow is one entry of the outage report.
type OutageRow struct {
	ID           string     `json:"id"`
	ResourceID   string     `json:"resource_id"`
	DisplayName  string     `json:"display_name"`
	TypeName     string     `json:"type_name"`
	Provider     string     `json:"provider"`
	Region       string     `json:"region"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
	DurationSec  int        `json:"duration_sec"`
	Severity     string     `json:"severity"`
	ClassifiedAs string     `json:"classified_as"`
	RootCause    string     `json:"root_cause,omitempty"`
}

// OutageReport is the outage report with its aggregates.
type OutageReport struct {
	From         string      `json:"from"`
	To           string      `json:"to"`
	Total        int         `json:"total"`
	Ongoing      int         `json:"ongoing"`
	TotalDownSec int         `json:"total_down_sec"`
	MeanMTTRSec  *int        `json:"mean_mttr_sec"`
	LongestSec   int         `json:"longest_sec"`
	Rows         []OutageRow `json:"rows"`
}

// PerformanceRow is one metric's statistics over the window.
type PerformanceRow struct {
	ResourceID  string  `json:"resource_id"`
	DisplayName string  `json:"display_name"`
	TypeName    string  `json:"type_name"`
	Provider    string  `json:"provider"`
	MetricKey   string  `json:"metric_key"`
	Label       string  `json:"label"`
	Unit        string  `json:"unit"`
	Avg         float64 `json:"avg"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	// P95 is the 95th percentile. Reported alongside the mean because an average
	// hides the spikes that people actually notice.
	P95      float64  `json:"p95"`
	Samples  int      `json:"samples"`
	Trouble  *float64 `json:"trouble,omitempty"`
	Critical *float64 `json:"critical,omitempty"`
	// Breaching reports whether P95 is already past a threshold, which is the
	// figure worth acting on rather than a single momentary spike.
	Breaching string `json:"breaching,omitempty"`
}

// PerformanceReport is the performance report.
type PerformanceReport struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	MetricKey string           `json:"metric_key"`
	Rows      []PerformanceRow `json:"rows"`
	// AvailableMetrics lists what else can be charted for the current scope, so
	// the UI can offer a metric selector without a separate request.
	AvailableMetrics []MetricOption `json:"available_metrics"`
}

// MetricOption is a selectable metric.
type MetricOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
	Count int    `json:"resource_count"`
}

// SLARow is one SLA definition measured against actual availability.
type SLARow struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	TargetPct     float64  `json:"target_pct"`
	ActualPct     *float64 `json:"actual_pct"`
	Compliant     *bool    `json:"compliant"`
	ResourceCount int      `json:"resource_count"`
	Period        string   `json:"period"`
	// ErrorBudgetSec is how much downtime the target still allows over the window.
	// Negative means the budget is already spent, which is the number an operator
	// wants rather than a percentage they have to convert in their head.
	ErrorBudgetSec *int `json:"error_budget_sec"`
	DownSec        int  `json:"down_sec"`
}

// SLAReport is the SLA report.
type SLAReport struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Rows []SLARow `json:"rows"`
}

// ReportFilter narrows a report to part of the estate.
type ReportFilter struct {
	Range    Range
	Provider []string
	Type     []string
	GroupID  string
	// Limit caps the returned rows; the aggregates still cover everything.
	Limit int
	// Worst orders by availability ascending rather than by name, which is what
	// a "bottom N" view is.
	Worst bool
	// IncludeDaily attaches the per-day series for a trend line. Off by default
	// because it multiplies the payload by the number of days.
	IncludeDaily bool
}

// Reporter is implemented by backends that can answer reporting queries.
type Reporter interface {
	AvailabilityReport(ctx context.Context, f ReportFilter) (AvailabilitySummary, error)
	OutageReport(ctx context.Context, f ReportFilter) (OutageReport, error)
	PerformanceReport(ctx context.Context, metricKey string, f ReportFilter) (PerformanceReport, error)
	SLAReport(ctx context.Context, f ReportFilter) (SLAReport, error)
}

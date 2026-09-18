package store

import (
	"context"
	"time"

	"nimbuseye/internal/model"
)

// Home section types and contract.
//
// These back the Home features the reference console groups together: maintenance
// windows, SLA targets, monitor groups and the alert delivery log. Every table
// already existed — the alerter has been honouring maintenance windows and reading
// sla_definitions since it was written — so what was missing was any way to create
// or see them outside of psql. A capability that only exists in the database is a
// capability the product does not have.
//
// Like Reporter and Administrator, this is implemented only by the PostgreSQL
// backend.

/* -------------------------------------------------------------------------- */
/* Maintenance windows                                                         */
/* -------------------------------------------------------------------------- */

// MaintenanceWindow suppresses alerting for a set of resources over a period.
type MaintenanceWindow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`

	ResourceIDs []string `json:"resource_ids"`
	GroupIDs    []string `json:"group_ids"`

	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`

	Recurrence     string `json:"recurrence,omitempty"`
	SuppressAlerts bool   `json:"suppress_alerts"`
	ExcludeFromSLA bool   `json:"exclude_from_sla"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// State is derived, not stored: scheduled, active or finished. Computed here
	// rather than in the UI so every caller agrees on what "active" means.
	State string `json:"state"`
	// ResourceCount is how many monitors the window actually covers once groups
	// are expanded. A window scoped to an empty group suppresses nothing, and that
	// is invisible from the scope alone.
	ResourceCount int `json:"resource_count"`
	// Names of a few covered monitors, so the list is readable without drilling in.
	SampleNames []string `json:"sample_names,omitempty"`
}

// MaintenanceInput creates or edits a window.
type MaintenanceInput struct {
	DisplayName    string    `json:"display_name"`
	ResourceIDs    []string  `json:"resource_ids"`
	GroupIDs       []string  `json:"group_ids"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	Recurrence     string    `json:"recurrence"`
	SuppressAlerts *bool     `json:"suppress_alerts"`
	ExcludeFromSLA *bool     `json:"exclude_from_sla"`
}

/* -------------------------------------------------------------------------- */
/* SLA targets                                                                 */
/* -------------------------------------------------------------------------- */

// SLATarget is an availability commitment for a set of monitors.
type SLATarget struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	TargetPct   float64 `json:"target_pct"`
	Period      string  `json:"period"`

	ResourceIDs []string `json:"resource_ids"`
	GroupIDs    []string `json:"group_ids"`

	CreatedAt time.Time `json:"created_at"`

	// ResourceCount is the expanded scope. Zero means the target measures nothing,
	// which the SLA report would otherwise show as an unexplained blank row.
	ResourceCount int `json:"resource_count"`
	// AllowedDownSecPerDay is the target restated as time. A person can sanity
	// check "four minutes a day"; they cannot sanity check 99.7 percent.
	AllowedDownSecPerDay int `json:"allowed_down_sec_per_day"`
}

// SLAInput creates or edits a target.
type SLAInput struct {
	DisplayName string   `json:"display_name"`
	TargetPct   float64  `json:"target_pct"`
	Period      string   `json:"period"`
	ResourceIDs []string `json:"resource_ids"`
	GroupIDs    []string `json:"group_ids"`
}

/* -------------------------------------------------------------------------- */
/* Monitor groups                                                              */
/* -------------------------------------------------------------------------- */

// MonitorGroup is a named set of monitors with a derived health state.
type MonitorGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`

	HealthStrategy  string `json:"health_strategy"`
	HealthThreshold *int   `json:"health_threshold"`

	CreatedAt time.Time `json:"created_at"`

	MemberCount int `json:"member_count"`
	// Counts by status, so the list shows health without a second request.
	Up        int `json:"up"`
	Down      int `json:"down"`
	Trouble   int `json:"trouble"`
	Critical  int `json:"critical"`
	Unknown   int `json:"unknown"`
	Suspended int `json:"suspended"`
	// Health is the group's own state under its strategy.
	Health string `json:"health"`
}

// GroupInput creates or edits a group.
type GroupInput struct {
	DisplayName     string   `json:"display_name"`
	Description     string   `json:"description"`
	HealthStrategy  string   `json:"health_strategy"`
	HealthThreshold *int     `json:"health_threshold"`
	ResourceIDs     []string `json:"resource_ids"`
}

/* -------------------------------------------------------------------------- */
/* Alert delivery log                                                         */
/* -------------------------------------------------------------------------- */

// AlertLogEntry is one attempt to tell somebody about an alert.
type AlertLogEntry struct {
	ID    int64  `json:"id"`
	State string `json:"state"`

	AlertID     string `json:"alert_id"`
	Severity    string `json:"severity"`
	MetricKey   string `json:"metric_key,omitempty"`
	ResourceID  string `json:"resource_id"`
	DisplayName string `json:"display_name"`

	ChannelID   string `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
	Recipient   string `json:"recipient,omitempty"`

	Level     int        `json:"level"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error,omitempty"`
	SentAt    *time.Time `json:"sent_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// AlertLogPage is a page of delivery attempts with its aggregates.
type AlertLogPage struct {
	Entries []AlertLogEntry `json:"entries"`
	// Counts by state across the whole filter, not just this page.
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Pending int `json:"pending"`
	// NextBefore is a keyset cursor; zero when there is no further page.
	NextBefore int64 `json:"next_before"`
	// DeliveryConfigured reports whether any channel is enabled at all. Without
	// one, every row here will read "skipped", and the reason is the absence of a
	// channel rather than anything about the alert.
	DeliveryConfigured bool `json:"delivery_configured"`
}

// AlertLogFilter narrows the delivery log.
type AlertLogFilter struct {
	State  string
	Before int64
	Limit  int
}

/* -------------------------------------------------------------------------- */
/* Outages                                                                     */
/* -------------------------------------------------------------------------- */

// OutageFilter narrows the outage list for the Home page.
type OutageFilter struct {
	// Ongoing restricts to outages that have not ended.
	Ongoing bool
	Since   *time.Time
	Limit   int
	Offset  int
}

// OutagePage is a page of outages with aggregates over the whole filter.
type OutagePage struct {
	Rows    []OutageRow `json:"rows"`
	Total   int         `json:"total"`
	Ongoing int         `json:"ongoing"`
	// MeanMTTRSec covers closed outages only; an ongoing one has no recovery time
	// yet and averaging it in would understate the figure.
	MeanMTTRSec  *int `json:"mean_mttr_sec"`
	TotalDownSec int  `json:"total_down_sec"`
}

// HomeStore is implemented by backends that support the Home section features.
type HomeStore interface {
	MaintenanceWindows(ctx context.Context) ([]MaintenanceWindow, error)
	CreateMaintenance(ctx context.Context, userID string, in MaintenanceInput) (MaintenanceWindow, error)
	DeleteMaintenance(ctx context.Context, id string) error

	SLATargets(ctx context.Context) ([]SLATarget, error)
	CreateSLATarget(ctx context.Context, in SLAInput) (SLATarget, error)
	DeleteSLATarget(ctx context.Context, id string) error

	MonitorGroups(ctx context.Context) ([]MonitorGroup, error)
	CreateMonitorGroup(ctx context.Context, in GroupInput) (MonitorGroup, error)
	UpdateMonitorGroup(ctx context.Context, id string, in GroupInput) (MonitorGroup, error)
	DeleteMonitorGroup(ctx context.Context, id string) error
	GroupMembers(ctx context.Context, id string) ([]model.Resource, error)

	AlertLog(ctx context.Context, f AlertLogFilter) (AlertLogPage, error)
	OutageList(ctx context.Context, f OutageFilter) (OutagePage, error)
}

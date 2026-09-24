package store

import (
	"context"
	"time"
)

// Configuration and inventory features.
//
// Business hours and tags sit on tables that already existed and had no way in:
// notification profiles carry a business_hours_id and could only ever say "always",
// and the collector has been importing cloud tags that nothing displayed.
//
// Discovered resources answers the question a monitoring tool should be able to
// answer and usually cannot — what does this account contain that nobody is
// watching. The collector already computes it every run; it just had nowhere to go.

/* -------------------------------------------------------------------------- */
/* Business hours                                                              */
/* -------------------------------------------------------------------------- */

// BusinessHoursSlot is one window within a week. Day follows ISO-8601: 1 is Monday.
type BusinessHoursSlot struct {
	Day   int    `json:"day"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// BusinessHours is a named weekly schedule used to decide when to notify.
type BusinessHours struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"display_name"`
	Timezone    string              `json:"timezone"`
	Slots       []BusinessHoursSlot `json:"slots"`
	CreatedAt   time.Time           `json:"created_at"`

	// UsedBy counts notification profiles referencing this schedule, so deleting
	// one that is in use can warn instead of silently widening those profiles to
	// "always".
	UsedBy int `json:"used_by"`
	// InHoursNow is evaluated server-side in the schedule's own timezone. Doing it
	// in the browser would use the viewer's clock, which is the wrong one.
	InHoursNow bool `json:"in_hours_now"`
}

// BusinessHoursInput creates or edits a schedule.
type BusinessHoursInput struct {
	DisplayName string              `json:"display_name"`
	Timezone    string              `json:"timezone"`
	Slots       []BusinessHoursSlot `json:"slots"`
}

/* -------------------------------------------------------------------------- */
/* Tags                                                                       */
/* -------------------------------------------------------------------------- */

// Tag is one key/value pair, either imported from the cloud or added here.
type Tag struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
	Color  string `json:"color,omitempty"`
	// Resources is how many monitors carry it. A tag on nothing is clutter, and
	// only the count makes that visible.
	Resources int `json:"resources"`
}

// TagKey groups a key with its values, which is how people actually think about
// tags: "environment" first, then which environments exist.
type TagKey struct {
	Key       string `json:"key"`
	Source    string `json:"source"`
	Values    []Tag  `json:"values"`
	Resources int    `json:"resources"`
}

/* -------------------------------------------------------------------------- */
/* Discovered resources                                                        */
/* -------------------------------------------------------------------------- */

// UnmappedType is a provider resource type discovery saw and could not monitor.
type UnmappedType struct {
	ProviderType string `json:"provider_type"`
	Count        int    `json:"count"`
}

// DiscoveryInventory is what an account contains versus what is monitored.
type DiscoveryInventory struct {
	AccountID   string     `json:"account_id"`
	AccountName string     `json:"account_name"`
	Provider    string     `json:"provider"`
	LastRunAt   *time.Time `json:"last_run_at"`
	Status      string     `json:"status,omitempty"`

	Discovered int `json:"discovered"`
	Monitored  int `json:"monitored"`
	// Ignored is deliberate noise: container images, backups, identity objects.
	// Separated from unmapped so the actionable list stays short.
	Ignored  int            `json:"ignored"`
	Unmapped []UnmappedType `json:"unmapped"`
	// UnmappedTotal is the number of individual resources behind those types.
	UnmappedTotal int `json:"unmapped_total"`
}

/* -------------------------------------------------------------------------- */
/* Health trend                                                                */
/* -------------------------------------------------------------------------- */

// TrendSeries is one monitor's availability over consecutive days.
type TrendSeries struct {
	ResourceID  string       `json:"resource_id"`
	DisplayName string       `json:"display_name"`
	TypeName    string       `json:"type_name"`
	Provider    string       `json:"provider"`
	Points      []TrendPoint `json:"points"`
	// Direction compares the second half of the window with the first:
	// improving, worsening or steady. Null when there is too little data to say,
	// which is more honest than calling two days a trend.
	Direction *string  `json:"direction"`
	First     *float64 `json:"first_pct"`
	Last      *float64 `json:"last_pct"`
}

// TrendPoint is one day of a trend.
type TrendPoint struct {
	Day             string   `json:"day"`
	AvailabilityPct *float64 `json:"availability_pct"`
	DownSec         int      `json:"down_sec"`
}

// HealthTrend is the trend report.
type HealthTrend struct {
	From string        `json:"from"`
	To   string        `json:"to"`
	Days int           `json:"days"`
	Rows []TrendSeries `json:"rows"`
	// MeasuredDays is how many of the requested days carry any data at all. The
	// report states it because a trend over two measured days is not a trend, and
	// the reader deserves to know that before drawing a conclusion.
	MeasuredDays int `json:"measured_days"`
}

/* -------------------------------------------------------------------------- */
/* Bulk action                                                                 */
/* -------------------------------------------------------------------------- */

// BulkActionInput applies one operation to many monitors.
type BulkActionInput struct {
	Action      string   `json:"action"`
	ResourceIDs []string `json:"resource_ids"`
}

// BulkActionResult reports what happened, per monitor where it failed.
type BulkActionResult struct {
	Action    string `json:"action"`
	Requested int    `json:"requested"`
	Applied   int    `json:"applied"`
	// Skipped carries a reason per monitor. Reported rather than swallowed: a bulk
	// operation that silently half-applied is worse than one that failed outright.
	Skipped map[string]string `json:"skipped,omitempty"`
}

// ConfigStore is implemented by backends supporting these features.
type ConfigStore interface {
	BusinessHours(ctx context.Context) ([]BusinessHours, error)
	CreateBusinessHours(ctx context.Context, in BusinessHoursInput) (BusinessHours, error)
	DeleteBusinessHours(ctx context.Context, id string) error
	SetProfileBusinessHours(ctx context.Context, profileID, businessHoursID string, notifyOutside bool) error

	TagInventory(ctx context.Context) ([]TagKey, error)

	DiscoveryInventory(ctx context.Context) ([]DiscoveryInventory, error)

	HealthTrend(ctx context.Context, f ReportFilter) (HealthTrend, error)

	BulkAction(ctx context.Context, in BulkActionInput) (BulkActionResult, error)

	CloudInventory(ctx context.Context, accountID string) (CloudInventory, error)
}

/* -------------------------------------------------------------------------- */
/* Cloud inventory                                                             */
/* -------------------------------------------------------------------------- */

// InventoryType is one resource type's footprint in an account.
type InventoryType struct {
	Code        string   `json:"code"`
	DisplayName string   `json:"display_name"`
	Category    string   `json:"category"`
	Count       int      `json:"count"`
	Regions     []string `json:"regions"`
	// Status counts, so the dashboard shows health alongside footprint. A type with
	// forty resources and nine unknown is a different situation from forty healthy
	// ones, and a count alone hides that.
	Up        int `json:"up"`
	Down      int `json:"down"`
	Trouble   int `json:"trouble"`
	Critical  int `json:"critical"`
	Unknown   int `json:"unknown"`
	Suspended int `json:"suspended"`
}

// InventoryRegion is a region's share of the account.
type InventoryRegion struct {
	Region string `json:"region"`
	Count  int    `json:"count"`
	Types  int    `json:"types"`
}

// CloudInventory is the Inventory Dashboard for one account.
type CloudInventory struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Provider    string `json:"provider"`

	Monitored int `json:"monitored"`
	// Discovered and Ignored come from the last collection, so the dashboard can
	// show what is monitored against what exists. A monitoring tool that only
	// reports on what it watches cannot tell you what it is missing.
	Discovered    int               `json:"discovered"`
	Ignored       int               `json:"ignored"`
	UnmappedTotal int               `json:"unmapped_total"`
	Unmapped      []UnmappedType    `json:"unmapped,omitempty"`
	LastRunAt     *time.Time        `json:"last_run_at"`
	Types         []InventoryType   `json:"types"`
	Regions       []InventoryRegion `json:"regions"`
}

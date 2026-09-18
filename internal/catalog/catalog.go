// Package catalog exposes the resource type catalog: what NimbusEye can monitor,
// which metrics each type has, and the default alert thresholds for them.
//
// catalog.json is the single source of truth and is embedded into the binary, so
// a deployed instance has no runtime dependency on a file or a database to know
// what a resource type is. db/migrations/004_catalog.sql loads the same content
// into PostgreSQL for the UI's own queries; the two are kept in step by
// tools/gencatalog.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

//go:embed catalog.json
var files embed.FS

// Metric describes one measurable series on a resource type.
type Metric struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	Unit           string   `json:"unit"`
	ProviderMetric string   `json:"provider_metric"`
	Namespace      string   `json:"namespace"`
	Statistic      string   `json:"statistic"`
	Trouble        *float64 `json:"trouble"`
	Critical       *float64 `json:"critical"`
	// HigherIsWorse is false for metrics where a LOW reading is the problem,
	// such as free disk space or healthy backend count. The alert evaluator
	// inverts the comparison for these, which is easy to get wrong if the
	// direction is inferred from the metric name instead of declared.
	HigherIsWorse bool `json:"higher_is_worse"`

	// IntervalSec is how often the provider publishes this metric, when that is
	// slower than the collector's default polling window. Zero means the default.
	//
	// This is not a tuning knob, it is a correctness requirement. Object Storage
	// publishes StoredBytes and ObjectCount roughly hourly. Asking for a
	// thirty-minute window at five-minute resolution returns an empty series most
	// of the time — not an error, just nothing — so twenty-two buckets reported no
	// metrics at all while every query succeeded. A metric's cadence is a property
	// of the metric, so it is declared here rather than guessed by the collector.
	IntervalSec int `json:"interval_sec,omitempty"`
}

// Window returns the lookback a query for this metric needs, given the caller's
// default. A metric published hourly needs at least three intervals to be sure of
// catching one, because publication is not aligned to when we happen to ask.
func (m Metric) Window(def time.Duration) time.Duration {
	if m.IntervalSec <= 0 {
		return def
	}
	need := time.Duration(m.IntervalSec) * 3 * time.Second
	if need > def {
		return need
	}
	return def
}

// Resolution returns the aggregation step for this metric. Asking for a step finer
// than the publication interval buckets most of the window into nothing.
func (m Metric) Resolution() string {
	switch {
	case m.IntervalSec >= 86400:
		return "1d"
	case m.IntervalSec >= 3600:
		return "1h"
	default:
		return "5m"
	}
}

// Type is one monitorable kind of thing, e.g. an OCI compute instance.
type Type struct {
	Code                 string   `json:"code"`
	Provider             string   `json:"provider"`
	Category             string   `json:"category"`
	DisplayName          string   `json:"display_name"`
	Icon                 string   `json:"icon"`
	SupportsAvailability bool     `json:"supports_availability"`
	SupportsMetrics      bool     `json:"supports_metrics"`
	SupportsCost         bool     `json:"supports_cost"`
	DefaultPollSec       int      `json:"default_poll_sec"`
	Metrics              []Metric `json:"metrics"`
}

// Metric returns the named metric definition for this type.
func (t Type) Metric(key string) (Metric, bool) {
	for _, m := range t.Metrics {
		if m.Key == key {
			return m, true
		}
	}
	return Metric{}, false
}

var (
	all        []Type
	byCode     map[string]Type
	Providers  []string
	Categories []string
)

func init() {
	b, err := files.ReadFile("catalog.json")
	if err != nil {
		panic(fmt.Sprintf("catalog: embedded catalog.json unreadable: %v", err))
	}
	if err := json.Unmarshal(b, &all); err != nil {
		panic(fmt.Sprintf("catalog: catalog.json is not valid: %v", err))
	}
	if len(all) == 0 {
		panic("catalog: catalog.json is empty")
	}

	byCode = make(map[string]Type, len(all))
	provSet := map[string]bool{}
	catSet := map[string]bool{}
	for _, t := range all {
		if _, dup := byCode[t.Code]; dup {
			panic("catalog: duplicate resource type code " + t.Code)
		}
		byCode[t.Code] = t
		provSet[t.Provider] = true
		catSet[t.Category] = true
	}
	for p := range provSet {
		Providers = append(Providers, p)
	}
	for c := range catSet {
		Categories = append(Categories, c)
	}
	sort.Strings(Providers)
	sort.Strings(Categories)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].DisplayName < all[j].DisplayName
	})
}

// All returns every resource type, sorted by provider then display name.
func All() []Type { return all }

// Get looks up a resource type by its code.
func Get(code string) (Type, bool) {
	t, ok := byCode[code]
	return t, ok
}

// ByProvider returns the resource types belonging to one cloud provider.
func ByProvider(provider string) []Type {
	var out []Type
	for _, t := range all {
		if t.Provider == provider {
			out = append(out, t)
		}
	}
	return out
}

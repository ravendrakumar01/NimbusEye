package demo

import (
	"fmt"
	"sort"
	"time"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/collect"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// This file implements the write side used by the collector.
//
// It exists so the collector can be run end to end before PostgreSQL is
// provisioned: real discovered resources land in the same store the UI reads, and
// swapping in a database store later means implementing this same small surface
// against SQL rather than rewriting the collector.
//
// The in-memory store is not a substitute for a database. Everything here is lost
// on restart, and metric samples are capped rather than retained.

// Compile-time assertion: this backend implements the storage contract. If a
// method is added to store.Store, the build breaks here rather than at the call
// site, which makes the omission obvious.
var _ store.Store = (*Store)(nil)

// maxSamplesPerSeries bounds memory use. A real deployment writes these to
// VictoriaMetrics instead, which is why the cap is small and unapologetic.
const maxSamplesPerSeries = 2000

// UpsertResources merges a collection run's resources into the store.
//
// Identity is (cloud_account_id, native_id): display names change, OCIDs do not.
// Resources that were present before and absent now are marked deleted rather
// than removed, so their availability history and cost attribution survive.
func (s *Store) UpsertResources(accountID string, incoming []model.Resource) (created, updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var acct *model.CloudAccount
	for i := range s.accounts {
		if s.accounts[i].ID == accountID {
			acct = &s.accounts[i]
			break
		}
	}
	if acct == nil {
		return 0, 0, fmt.Errorf("unknown cloud account %q", accountID)
	}

	// Index what this account currently has.
	existing := map[string]int{}
	for i := range s.resources {
		if s.resources[i].CloudAccountID == accountID {
			existing[s.resources[i].NativeID] = i
		}
	}

	now := time.Now().UTC()
	seen := map[string]bool{}

	for _, in := range incoming {
		if in.NativeID == "" {
			continue
		}
		seen[in.NativeID] = true

		// Fill in the presentation fields from the catalog rather than trusting
		// the collector to send them; one source of truth for type metadata.
		t, ok := catalog.Get(in.ResourceType)
		if !ok {
			continue
		}
		in.CloudAccountID = accountID
		in.Provider = t.Provider
		in.TypeName = t.DisplayName
		in.Category = t.Category
		in.LastSeenAt = now

		if idx, found := existing[in.NativeID]; found {
			cur := &s.resources[idx]
			// Preserve fields the collector does not own.
			in.ID = cur.ID
			in.GroupIDs = cur.GroupIDs
			in.Availability24h = cur.Availability24h
			in.OpenAlarms = cur.OpenAlarms
			in.DiscoveredAt = cur.DiscoveredAt
			// Same two rules as the PostgreSQL backend, kept in step so mock mode
			// still predicts production.
			//
			// A pass that returns no metric is not evidence a resource stopped
			// working, so an incoming "unknown" does not overwrite a recently
			// confirmed "up".
			if in.Status == model.StatusUnknown && cur.Status == model.StatusUp &&
				!cur.ConfirmedAt.IsZero() && now.Sub(cur.ConfirmedAt) < store.ConfirmationGrace {
				in.Status = model.StatusUp
			}
			if in.Status == model.StatusUp {
				in.ConfirmedAt = now
			} else {
				in.ConfirmedAt = cur.ConfirmedAt
			}
			// status_since only moves on a real change.
			if cur.Status == in.Status {
				in.StatusSince = cur.StatusSince
			} else {
				in.StatusSince = now
			}
			in.DeletedAt = nil
			*cur = in
			updated++
		} else {
			in.ID = fmt.Sprintf("res-%s-%d", accountID, len(s.resources)+1)
			in.DiscoveredAt = now
			if in.Status == model.StatusUp {
				in.ConfirmedAt = now
			}
			if in.StatusSince.IsZero() {
				in.StatusSince = now
			}
			s.resources = append(s.resources, in)
			created++
		}
	}

	// Soft-delete what discovery no longer sees.
	for nativeID, idx := range existing {
		if seen[nativeID] {
			continue
		}
		if s.resources[idx].DeletedAt == nil {
			s.resources[idx].DeletedAt = &now
		}
	}

	// Rebuild the id index; slice growth may have moved elements.
	s.byID = make(map[string]*model.Resource, len(s.resources))
	for i := range s.resources {
		s.byID[s.resources[i].ID] = &s.resources[i]
	}

	s.indexTags()
	s.rollUpAccounts()
	s.rollUpGroups()
	return created, updated, nil
}

// PutSamples stores metric samples in memory, newest last, capped per series.
func (s *Store) PutSamples(samples []collect.Sample) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.samples == nil {
		s.samples = map[string][]model.Sample{}
	}
	stored := 0
	for _, sp := range samples {
		key := sp.NativeID + "|" + sp.MetricKey
		s.samples[key] = append(s.samples[key], model.Sample{T: sp.T, V: sp.V})
		stored++
	}
	for key, series := range s.samples {
		sort.Slice(series, func(i, j int) bool { return series[i].T.Before(series[j].T) })
		if len(series) > maxSamplesPerSeries {
			series = series[len(series)-maxSamplesPerSeries:]
		}
		s.samples[key] = series
	}
	return stored, nil
}

// RecordRun updates an account's discovery health from a collection run.
func (s *Store) RecordRun(run collect.RunReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.accounts {
		if s.accounts[i].ID != run.AccountID {
			continue
		}
		a := &s.accounts[i]
		finished := run.FinishedAt
		a.LastDiscoveryAt = &finished
		a.DiscoveryState = run.State
		a.LastError = run.Error
		if run.State == "partial" && a.LastError == "" && len(run.RegionsFailed) > 0 {
			// Never leave a degraded account without a reason on screen.
			a.LastError = fmt.Sprintf("could not reach %d of %d regions: %v",
				len(run.RegionsFailed), len(run.RegionsFailed)+len(run.RegionsOK), run.RegionsFailed)
		}
		return nil
	}
	return fmt.Errorf("unknown cloud account %q", run.AccountID)
}

// realSeries returns collected samples for a resource metric, if any exist.
// Used by Metrics to prefer real data over generated data once a collector has
// run, so a live instance never silently shows synthetic numbers.
func (s *Store) realSeries(nativeID, metricKey string, from, to time.Time) []model.Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	series := s.samples[nativeID+"|"+metricKey]
	if len(series) == 0 {
		return nil
	}
	var out []model.Sample
	for _, sp := range series {
		if sp.T.Before(from) || sp.T.After(to) {
			continue
		}
		out = append(out, sp)
	}
	return out
}

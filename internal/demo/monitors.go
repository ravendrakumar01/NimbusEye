package demo

import (
	"fmt"
	"strings"
	"time"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// User-created monitors for the in-memory backend.
//
// Same validation as the PostgreSQL backend — it lives in internal/store and is
// called by both — so mock mode accepts and rejects exactly what production does.
// Only the persistence differs.

// CreateMonitor adds a user-created monitor.
func (s *Store) CreateMonitor(in store.MonitorInput) (model.Resource, error) {
	if err := store.ValidateMonitor(in); err != nil {
		return model.Resource{}, err
	}
	t, _ := catalog.Get(in.ResourceType)
	nativeID := store.NewSyntheticNativeID()

	s.mu.Lock()
	defer s.mu.Unlock()

	name := strings.ToLower(strings.TrimSpace(in.DisplayName))
	for _, r := range s.resources {
		if r.DeletedAt == nil && r.CloudAccountID == "" &&
			strings.ToLower(r.DisplayName) == name {
			return model.Resource{}, &store.ValidationError{Fields: map[string]string{
				"display_name": "A monitor with this name already exists.",
			}}
		}
	}

	now := time.Now().UTC()
	tags := map[string]string{}
	for k, v := range in.Tags {
		if k = strings.TrimSpace(k); k != "" {
			tags[k] = strings.TrimSpace(v)
		}
	}

	res := model.Resource{
		ID:           fmt.Sprintf("res-mon-%d", len(s.resources)+1),
		Provider:     t.Provider,
		ResourceType: t.Code,
		TypeName:     t.DisplayName,
		Category:     t.Category,
		NativeID:     nativeID,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		Region:       "global",
		// Configured but never checked. Reporting 'up' here would show a green
		// monitor that has not run once.
		Status:       model.StatusDiscovery,
		StatusSince:  now,
		Tags:         tags,
		Attributes:   map[string]any{"check_config": store.BuildCheckConfig(in)},
		DiscoveredAt: now,
		LastSeenAt:   now,
	}
	s.resources = append(s.resources, res)

	s.byID = make(map[string]*model.Resource, len(s.resources))
	for i := range s.resources {
		s.byID[s.resources[i].ID] = &s.resources[i]
	}
	s.indexTags()
	s.rollUpGroups()
	return res, nil
}

// UpdateMonitor edits a user-created monitor.
func (s *Store) UpdateMonitor(id string, in store.MonitorInput) (model.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.byID[id]
	if !ok || cur.DeletedAt != nil {
		return model.Resource{}, store.ErrResourceNotFound
	}
	if cur.CloudAccountID != "" {
		return model.Resource{}, &store.ValidationError{Fields: map[string]string{
			"resource_type": "This resource is discovered from a cloud account and cannot be edited here.",
		}}
	}
	in.ResourceType = cur.ResourceType
	if err := store.ValidateMonitor(in); err != nil {
		return model.Resource{}, err
	}

	name := strings.ToLower(strings.TrimSpace(in.DisplayName))
	for i := range s.resources {
		r := &s.resources[i]
		if r.ID != id && r.DeletedAt == nil && r.CloudAccountID == "" &&
			strings.ToLower(r.DisplayName) == name {
			return model.Resource{}, &store.ValidationError{Fields: map[string]string{
				"display_name": "Another monitor already has this name.",
			}}
		}
	}

	// native_id is left alone so the monitor keeps its metric history.
	cur.DisplayName = strings.TrimSpace(in.DisplayName)
	cur.Attributes = map[string]any{"check_config": store.BuildCheckConfig(in)}
	tags := map[string]string{}
	for k, v := range in.Tags {
		if k = strings.TrimSpace(k); k != "" {
			tags[k] = strings.TrimSpace(v)
		}
	}
	cur.Tags = tags
	s.indexTags()
	return *cur, nil
}

// DeleteMonitor soft-deletes a user-created monitor and clears its open state.
func (s *Store) DeleteMonitor(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.byID[id]
	if !ok || cur.DeletedAt != nil {
		return store.ErrResourceNotFound
	}
	if cur.CloudAccountID != "" {
		return fmt.Errorf("resource is discovered from a cloud account; remove the account instead")
	}
	now := time.Now().UTC()
	cur.DeletedAt = &now
	s.closeOpenState(id, now)
	s.rollUpAccounts()
	s.rollUpGroups()
	return nil
}

// SetSuspended activates or suspends a monitor.
func (s *Store) SetSuspended(id string, suspended bool) (model.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.byID[id]
	if !ok || cur.DeletedAt != nil {
		return model.Resource{}, store.ErrResourceNotFound
	}
	now := time.Now().UTC()
	cur.Suspended = suspended
	if suspended {
		cur.Status = model.StatusSuspended
		// Suspending means "stop telling me about this", so open alarms go too.
		s.closeOpenState(id, now)
	} else {
		cur.Status = model.StatusDiscovery
	}
	cur.StatusSince = now
	return *cur, nil
}

// closeOpenState resolves alarms and closes outages for a resource. Caller holds
// the lock.
func (s *Store) closeOpenState(resourceID string, now time.Time) {
	for i := range s.alarms {
		a := &s.alarms[i]
		if a.ResourceID == resourceID && (a.State == model.AlarmOpen || a.State == model.AlarmAcknowledged) {
			a.State = model.AlarmResolved
			a.ResolvedAt = &now
		}
	}
	for i := range s.outages {
		o := &s.outages[i]
		if o.ResourceID == resourceID && o.EndedAt == nil {
			o.EndedAt = &now
			o.DurationSec = int(now.Sub(o.StartedAt).Seconds())
		}
	}
	if r, ok := s.byID[resourceID]; ok {
		r.OpenAlarms = 0
	}
}

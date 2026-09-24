package httpapi

import (
	"net/http"
	"strings"

	"nimbuseye/internal/store"
)

// Configuration and inventory endpoints.
//
// Business hours and tags are administration, so they need the admin role to
// change. Bulk actions need only operator: suspending a set of monitors during a
// migration is operational work, and the cap in the store keeps a mis-click from
// taking out an estate.

func (s *Server) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/business-hours", s.listBusinessHours)
	mux.HandleFunc("POST /api/v1/admin/business-hours", s.createBusinessHours)
	mux.HandleFunc("DELETE /api/v1/admin/business-hours/{id}", s.deleteBusinessHours)
	mux.HandleFunc("PATCH /api/v1/admin/notification-profiles/{id}/business-hours",
		s.setProfileBusinessHours)

	mux.HandleFunc("GET /api/v1/admin/tags", s.listTags)
	mux.HandleFunc("GET /api/v1/discovered", s.listDiscovered)
	mux.HandleFunc("GET /api/v1/reports/health-trend", s.reportHealthTrend)
	mux.HandleFunc("POST /api/v1/monitors/bulk", s.bulkAction)
	mux.HandleFunc("GET /api/v1/cloud-accounts/{id}/inventory", s.cloudInventory)
}

func (s *Server) config(w http.ResponseWriter) (store.ConfigStore, bool) {
	c, ok := s.store.(store.ConfigStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unavailable",
			"This needs the database backend.")
		return nil, false
	}
	return c, true
}

/* -------------------------------------------------------------------------- */
/* Business hours                                                              */
/* -------------------------------------------------------------------------- */

func (s *Server) listBusinessHours(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	hs, err := c.BusinessHours(r.Context())
	if err != nil {
		s.adminError(w, "list business hours", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": hs, "count": len(hs)})
}

func (s *Server) createBusinessHours(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.BusinessHoursInput
	if !decode(w, r, &in) {
		return
	}
	b, err := c.CreateBusinessHours(r.Context(), in)
	if err != nil {
		s.adminError(w, "create business hours", err)
		return
	}
	s.audit(r, actor, "business_hours.create", "business_hours", b.ID, map[string]any{
		"name": b.DisplayName, "timezone": b.Timezone, "windows": len(b.Slots),
	})
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) deleteBusinessHours(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := c.DeleteBusinessHours(r.Context(), id); err != nil {
		s.adminError(w, "delete business hours", err)
		return
	}
	s.audit(r, actor, "business_hours.delete", "business_hours", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) setProfileBusinessHours(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		BusinessHoursID string `json:"business_hours_id"`
		NotifyOutside   bool   `json:"notify_outside_business_hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if err := c.SetProfileBusinessHours(r.Context(), id, in.BusinessHoursID, in.NotifyOutside); err != nil {
		s.adminError(w, "set profile business hours", err)
		return
	}
	s.audit(r, actor, "notification_profile.business_hours", "notification_profile", id,
		map[string]any{"business_hours_id": in.BusinessHoursID, "notify_outside": in.NotifyOutside})
	writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

/* -------------------------------------------------------------------------- */
/* Tags, discovery, trend, bulk                                                */
/* -------------------------------------------------------------------------- */

func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	keys, err := c.TagInventory(r.Context())
	if err != nil {
		s.adminError(w, "list tags", err)
		return
	}
	values := 0
	for _, k := range keys {
		values += len(k.Values)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": keys, "key_count": len(keys), "value_count": values,
	})
}

func (s *Server) listDiscovered(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	inv, err := c.DiscoveryInventory(r.Context())
	if err != nil {
		s.adminError(w, "discovery inventory", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": inv})
}

func (s *Server) reportHealthTrend(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	f, bad := parseReportFilter(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, "invalid_range", bad)
		return
	}
	t, err := c.HealthTrend(r.Context(), f)
	if err != nil {
		s.adminError(w, "health trend", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) bulkAction(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in store.BulkActionInput
	if !decode(w, r, &in) {
		return
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	res, err := c.BulkAction(r.Context(), in)
	if err != nil {
		s.adminError(w, "bulk action", err)
		return
	}
	s.audit(r, actor, "monitors.bulk_"+res.Action, "resource", "", map[string]any{
		"requested": res.Requested, "applied": res.Applied, "skipped": len(res.Skipped),
	})
	writeJSON(w, http.StatusOK, res)
}

// cloudInventory backs the Cloud section's Inventory Dashboard.
func (s *Server) cloudInventory(w http.ResponseWriter, r *http.Request) {
	c, ok := s.config(w)
	if !ok {
		return
	}
	inv, err := c.CloudInventory(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminError(w, "cloud inventory", err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

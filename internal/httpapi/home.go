package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"nimbuseye/internal/store"
)

// Home section endpoints.
//
// Reads are open to any signed-in user. Writes need the operator role or above,
// which is a lower bar than administration on purpose: scheduling a maintenance
// window before a deployment is routine operational work, not configuration of the
// monitoring system itself. Deleting one is the same.

// operatorRoles may create maintenance windows, SLA targets and groups.
var operatorRoles = map[string]bool{"owner": true, "admin": true, "operator": true}

func (s *Server) registerHomeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/maintenance", s.listMaintenance)
	mux.HandleFunc("POST /api/v1/maintenance", s.createMaintenance)
	mux.HandleFunc("DELETE /api/v1/maintenance/{id}", s.deleteMaintenance)

	mux.HandleFunc("GET /api/v1/slo", s.listSLO)
	mux.HandleFunc("POST /api/v1/slo", s.createSLO)
	mux.HandleFunc("DELETE /api/v1/slo/{id}", s.deleteSLO)

	mux.HandleFunc("POST /api/v1/groups", s.createGroup)
	mux.HandleFunc("PATCH /api/v1/groups/{id}", s.updateGroup)
	mux.HandleFunc("DELETE /api/v1/groups/{id}", s.deleteGroup)
	mux.HandleFunc("GET /api/v1/groups/{id}/members", s.groupMembers)

	mux.HandleFunc("GET /api/v1/alert-logs", s.listAlertLogs)
	mux.HandleFunc("GET /api/v1/outage-list", s.outageList)
}

// home returns the Home store, or explains why it is unavailable.
func (s *Server) home(w http.ResponseWriter) (store.HomeStore, bool) {
	h, ok := s.store.(store.HomeStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "home_unavailable",
			"This needs the database backend. The in-memory store would accept the "+
				"change and discard it.")
		return nil, false
	}
	return h, true
}

// requireOperator gates writes and returns the acting user id.
func (s *Server) requireOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no_session", "Sign in to continue.")
		return "", false
	}
	if !operatorRoles[sess.User.Role] {
		writeError(w, http.StatusForbidden, "insufficient_role",
			"This needs the operator role or above. Yours is "+sess.User.Role+".")
		return "", false
	}
	return sess.User.ID, true
}

/* -------------------------------------------------------------------------- */
/* Maintenance                                                                 */
/* -------------------------------------------------------------------------- */

func (s *Server) listMaintenance(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	ws, err := h.MaintenanceWindows(r.Context())
	if err != nil {
		s.adminError(w, "list maintenance windows", err)
		return
	}
	active := 0
	for _, x := range ws {
		if x.State == "active" {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"windows": ws,
		// Surfaced separately because "are alerts suppressed right now" is the
		// question this page exists to answer.
		"active": active,
	})
}

func (s *Server) createMaintenance(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in store.MaintenanceInput
	if !decode(w, r, &in) {
		return
	}
	win, err := h.CreateMaintenance(r.Context(), actor, in)
	if err != nil {
		s.adminError(w, "create maintenance window", err)
		return
	}
	s.audit(r, actor, "maintenance.create", "maintenance_window", win.ID, map[string]any{
		"name": win.DisplayName, "starts_at": win.StartsAt, "ends_at": win.EndsAt,
		"monitors": win.ResourceCount, "suppress_alerts": win.SuppressAlerts,
	})
	writeJSON(w, http.StatusCreated, win)
}

func (s *Server) deleteMaintenance(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.DeleteMaintenance(r.Context(), id); err != nil {
		s.adminError(w, "delete maintenance window", err)
		return
	}
	s.audit(r, actor, "maintenance.delete", "maintenance_window", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

/* -------------------------------------------------------------------------- */
/* SLO                                                                         */
/* -------------------------------------------------------------------------- */

func (s *Server) listSLO(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	ts, err := h.SLATargets(r.Context())
	if err != nil {
		s.adminError(w, "list SLA targets", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": ts, "count": len(ts)})
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in store.SLAInput
	if !decode(w, r, &in) {
		return
	}
	t, err := h.CreateSLATarget(r.Context(), in)
	if err != nil {
		s.adminError(w, "create SLA target", err)
		return
	}
	s.audit(r, actor, "slo.create", "sla_definition", t.ID, map[string]any{
		"name": t.DisplayName, "target_pct": t.TargetPct,
		"period": t.Period, "monitors": t.ResourceCount,
	})
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.DeleteSLATarget(r.Context(), id); err != nil {
		s.adminError(w, "delete SLA target", err)
		return
	}
	s.audit(r, actor, "slo.delete", "sla_definition", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

/* -------------------------------------------------------------------------- */
/* Monitor groups                                                              */
/* -------------------------------------------------------------------------- */

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in store.GroupInput
	if !decode(w, r, &in) {
		return
	}
	g, err := h.CreateMonitorGroup(r.Context(), in)
	if err != nil {
		s.adminError(w, "create group", err)
		return
	}
	s.audit(r, actor, "group.create", "resource_group", g.ID, map[string]any{
		"name": g.DisplayName, "members": g.MemberCount,
	})
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in store.GroupInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	g, err := h.UpdateMonitorGroup(r.Context(), id, in)
	if err != nil {
		s.adminError(w, "update group", err)
		return
	}
	s.audit(r, actor, "group.update", "resource_group", id, map[string]any{
		"name": g.DisplayName, "members": g.MemberCount,
	})
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.DeleteMonitorGroup(r.Context(), id); err != nil {
		s.adminError(w, "delete group", err)
		return
	}
	s.audit(r, actor, "group.delete", "resource_group", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	members, err := h.GroupMembers(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminError(w, "list group members", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members, "count": len(members)})
}

/* -------------------------------------------------------------------------- */
/* Alert delivery log and outages                                              */
/* -------------------------------------------------------------------------- */

func (s *Server) listAlertLogs(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AlertLogFilter{State: strings.TrimSpace(q.Get("state"))}
	if n, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		f.Before = n
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	page, err := h.AlertLog(r.Context(), f)
	if err != nil {
		s.adminError(w, "read alert log", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) outageList(w http.ResponseWriter, r *http.Request) {
	h, ok := s.home(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.OutageFilter{Ongoing: q.Get("ongoing") == "1"}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = n
	}
	// Default to the last 30 days rather than all history: the page is for "what
	// has been breaking", and an unbounded scan grows without limit.
	days := 30
	if n, err := strconv.Atoi(q.Get("days")); err == nil && n > 0 && n <= 400 {
		days = n
	}
	since := time.Now().AddDate(0, 0, -days)
	f.Since = &since

	page, err := h.OutageList(r.Context(), f)
	if err != nil {
		s.adminError(w, "list outages", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

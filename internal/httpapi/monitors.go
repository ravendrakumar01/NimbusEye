package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/store"
)

// Monitor write endpoints: the Add Monitor flow and the per-monitor actions.
//
// Only synthetic types can be created. Cloud resources arrive from a collector,
// and accepting a hand-made one would put a monitor in every count that measures
// nothing.

func (s *Server) registerMonitorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/monitors/creatable", s.creatableTypes)
	mux.HandleFunc("POST /api/v1/monitors", s.createMonitor)
	mux.HandleFunc("PATCH /api/v1/monitors/{id}", s.updateMonitor)
	mux.HandleFunc("DELETE /api/v1/monitors/{id}", s.deleteMonitor)
	mux.HandleFunc("POST /api/v1/monitors/{id}/suspend", s.suspendMonitor)
	mux.HandleFunc("POST /api/v1/monitors/{id}/activate", s.activateMonitor)
}

// creatableTypes drives the Add Monitor catalog and its per-type form.
//
// Every creatable type is returned with the fields it needs, so the frontend
// renders the form from this rather than carrying its own copy of the rules. The
// non-creatable types are listed separately with the reason, because "why can I
// not add an EC2 instance here" is a fair question to answer in the UI.
func (s *Server) creatableTypes(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Code           string            `json:"code"`
		DisplayName    string            `json:"display_name"`
		Category       string            `json:"category"`
		Icon           string            `json:"icon"`
		Provider       string            `json:"provider"`
		Fields         []store.FieldSpec `json:"fields"`
		DefaultPollSec int               `json:"default_poll_sec"`
		Description    string            `json:"description"`
	}
	descriptions := map[string]string{
		"WEB_HTTP":          "Check that a URL responds, how fast, and that the page contains what it should.",
		"WEB_REST_API":      "Check an API endpoint's status code and response body.",
		"WEB_PING":          "ICMP echo to a host. Blocked by default on many cloud networks.",
		"WEB_PORT":          "TCP connect to a host and port, for services with no HTTP interface.",
		"WEB_DNS":           "Resolve a record and optionally assert the answer, catching stale or hijacked records.",
		"WEB_SSL_CERT":      "Watch a certificate's expiry so it never lapses unnoticed.",
		"WEB_DOMAIN_EXPIRY": "Watch a domain registration's expiry date.",
		"WEB_HEARTBEAT":     "Inbound check: a scheduled job calls NimbusEye, and a missed call raises the alarm.",
	}

	var creatable []entry
	for _, code := range store.CreatableTypes {
		t, ok := catalog.Get(code)
		if !ok {
			continue
		}
		creatable = append(creatable, entry{
			Code: t.Code, DisplayName: t.DisplayName, Category: t.Category,
			Icon: t.Icon, Provider: t.Provider,
			Fields: store.MonitorFields(code), DefaultPollSec: t.DefaultPollSec,
			Description: descriptions[code],
		})
	}

	type unavailable struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
		Provider    string `json:"provider"`
		Category    string `json:"category"`
		Reason      string `json:"reason"`
	}
	var discovered []unavailable
	for _, t := range catalog.All() {
		if store.IsCreatable(t.Code) {
			continue
		}
		reason := "Discovered automatically from a connected cloud account."
		if t.Provider == "k8s" {
			reason = "Reported by the in-cluster agent."
		}
		discovered = append(discovered, unavailable{
			Code: t.Code, DisplayName: t.DisplayName, Provider: t.Provider,
			Category: t.Category, Reason: reason,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"creatable":  creatable,
		"discovered": discovered,
	})
}

func decodeMonitorInput(r *http.Request) (store.MonitorInput, error) {
	var in store.MonitorInput
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return in, err
	}
	return in, nil
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	in, err := decodeMonitorInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	res, err := s.store.CreateMonitor(in)
	if err != nil {
		if s.writeValidation(w, err) {
			return
		}
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	s.log.Info("monitor created", "id", res.ID, "type", res.ResourceType, "name", res.DisplayName)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	in, err := decodeMonitorInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	res, err := s.store.UpdateMonitor(r.PathValue("id"), in)
	if err != nil {
		if s.writeValidation(w, err) {
			return
		}
		if errors.Is(err, store.ErrResourceNotFound) {
			writeError(w, http.StatusNotFound, "monitor_not_found", "No such monitor")
			return
		}
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMonitor(id); err != nil {
		if errors.Is(err, store.ErrResourceNotFound) {
			writeError(w, http.StatusNotFound, "monitor_not_found", "No such monitor")
			return
		}
		writeError(w, http.StatusConflict, "delete_failed", err.Error())
		return
	}
	s.log.Warn("monitor deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) suspendMonitor(w http.ResponseWriter, r *http.Request) {
	s.setSuspended(w, r, true)
}

func (s *Server) activateMonitor(w http.ResponseWriter, r *http.Request) {
	s.setSuspended(w, r, false)
}

func (s *Server) setSuspended(w http.ResponseWriter, r *http.Request, suspended bool) {
	res, err := s.store.SetSuspended(r.PathValue("id"), suspended)
	if err != nil {
		if errors.Is(err, store.ErrResourceNotFound) {
			writeError(w, http.StatusNotFound, "monitor_not_found", "No such monitor")
			return
		}
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	action := "activated"
	if suspended {
		action = "suspended"
	}
	s.log.Info("monitor "+action, "id", res.ID, "name", res.DisplayName)
	writeJSON(w, http.StatusOK, res)
}

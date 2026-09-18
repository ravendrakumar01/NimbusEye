// Package httpapi exposes the NimbusEye REST API.
//
// Routing uses the standard library's method+pattern mux (Go 1.22+), so the
// service has no third-party HTTP dependency. Handlers read from a Store, which
// in mock mode is the in-memory demo dataset and later will be PostgreSQL plus
// VictoriaMetrics behind the same interface.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nimbuseye/internal/auth"
	"nimbuseye/internal/catalog"
	"nimbuseye/internal/mail"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Store is the storage contract, defined in internal/store so that both the
// in-memory and PostgreSQL backends implement exactly the same surface.
type Store = store.Store

// Config controls server behaviour that differs between local development and
// the deployed instance.
type Config struct {
	// DevCORSOrigin allows the Vite dev server to call the API from a different
	// port. It must stay empty in production, where the UI is served from the
	// same origin by nginx.
	DevCORSOrigin string
	// Mock reports that responses come from generated data. It is surfaced in
	// /healthz and in a response header so a demo instance can never be mistaken
	// for one showing real infrastructure.
	Mock bool
	// IngestToken guards the collector-facing endpoints. When empty those routes
	// are not registered at all, so a default install cannot be written to by
	// anyone who can reach the port.
	IngestToken string
	// TenantSlug is the tenant this process serves. A session for any other
	// tenant is refused rather than silently served the wrong data.
	TenantSlug string
	// SecureCookies marks session cookies Secure. Must be true in production; it
	// is false only for plain-HTTP local development, because a Secure cookie is
	// never sent over HTTP and sign-in would be impossible.
	SecureCookies bool
	// BaseURL is the public origin, used to build links in outgoing email. A
	// relative link is useless in a mailbox.
	BaseURL string
	Version string
}

// Server holds the API dependencies.
type Server struct {
	store  Store
	auth   *auth.Store
	mailer *mail.Sender
	cfg    Config
	log    *slog.Logger
}

// New builds the server.
//
// authStore may be nil in mock mode, where there is no database to hold users. The
// middleware then refuses every protected route rather than allowing it.
// mailer may be nil, in which case anything that needs email says so plainly
// rather than silently doing nothing.
func New(store Store, authStore *auth.Store, mailer *mail.Sender, cfg Config, log *slog.Logger) *Server {
	return &Server{store: store, auth: authStore, mailer: mailer, cfg: cfg, log: log}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/catalog/resource-types", s.resourceTypes)
	mux.HandleFunc("GET /api/v1/catalog/filters", s.filters)
	mux.HandleFunc("GET /api/v1/status/summary", s.summary)
	mux.HandleFunc("GET /api/v1/resources", s.listResources)
	mux.HandleFunc("GET /api/v1/resources/{id}", s.getResource)
	mux.HandleFunc("GET /api/v1/resources/{id}/metrics", s.resourceMetrics)
	mux.HandleFunc("GET /api/v1/resources/{id}/outages", s.resourceOutages)
	mux.HandleFunc("GET /api/v1/alarms", s.listAlarms)
	mux.HandleFunc("POST /api/v1/alarms/{id}/acknowledge", s.ackAlarm)
	mux.HandleFunc("GET /api/v1/outages", s.listOutages)
	mux.HandleFunc("GET /api/v1/cloud-accounts", s.listAccounts)
	mux.HandleFunc("GET /api/v1/groups", s.listGroups)
	s.registerAccountRoutes(mux)
	s.registerMonitorRoutes(mux)
	s.registerIngestRoutes(mux)
	if s.auth != nil {
		s.registerAuthRoutes(mux)
		s.registerResetRoutes(mux)
	}

	// requireSession sits innermost so panics and request logging still work on a
	// rejected request, and outermost of the routing so no handler can be reached
	// without passing it.
	return s.recoverPanic(s.requestLog(s.cors(s.requireSession(mux))))
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
		"mode":    modeName(s.cfg.Mock),
		"time":    time.Now().UTC(),
	})
}

func (s *Server) resourceTypes(w http.ResponseWriter, r *http.Request) {
	types := catalog.All()
	if p := r.URL.Query().Get("provider"); p != "" {
		types = catalog.ByProvider(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": types,
		"total": len(types),
	})
}

// filters returns everything the UI needs to populate its filter controls in one
// request, instead of the four round trips the obvious design would need.
func (s *Server) filters(w http.ResponseWriter, r *http.Request) {
	type typeRef struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
		Provider    string `json:"provider"`
		Category    string `json:"category"`
		Icon        string `json:"icon"`
	}
	var types []typeRef
	for _, t := range catalog.All() {
		types = append(types, typeRef{t.Code, t.DisplayName, t.Provider, t.Category, t.Icon})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":  catalog.Providers,
		"categories": catalog.Categories,
		"statuses": []string{
			model.StatusDown, model.StatusCritical, model.StatusTrouble, model.StatusUp,
			model.StatusMaintenance, model.StatusSuspended, model.StatusDiscovery, model.StatusUnknown,
		},
		"severities":     []string{model.SeverityDown, model.SeverityCritical, model.SeverityTrouble, model.SeverityInfo},
		"resource_types": types,
		"regions":        s.store.Regions(),
		"tags":           s.store.Tags(),
		"groups":         s.store.Groups(),
	})
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Summary())
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ResourceFilter{
		Query:      q.Get("q"),
		Status:     csv(q.Get("status")),
		Provider:   csv(q.Get("provider")),
		Type:       csv(q.Get("type")),
		Category:   csv(q.Get("category")),
		Region:     csv(q.Get("region")),
		GroupID:    q.Get("group"),
		Tag:        q.Get("tag"),
		OnlyIssues: q.Get("issues") == "1" || q.Get("issues") == "true",
		Sort:       q.Get("sort"),
		Page:       atoiDefault(q.Get("page"), 1),
		PageSize:   atoiDefault(q.Get("page_size"), 50),
	}
	writeJSON(w, http.StatusOK, s.store.Resources(f))
}

func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := s.store.Resource(id)
	if !ok {
		writeError(w, http.StatusNotFound, "resource_not_found", "No resource with id "+id)
		return
	}
	// Attach the type definition so the detail page knows which charts to draw
	// without a second request against the catalog.
	t, _ := catalog.Get(res.ResourceType)
	alarms := s.store.Alarms(store.AlarmFilter{ResourceID: id, PageSize: 100})
	writeJSON(w, http.StatusOK, map[string]any{
		"resource": res,
		"type":     t,
		"alarms":   alarms.Items,
	})
}

func (s *Server) resourceMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()

	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	if !to.After(from) {
		writeError(w, http.StatusBadRequest, "invalid_range", "'to' must be after 'from'")
		return
	}
	points := atoiDefault(q.Get("points"), 288)

	keys := csv(q.Get("metric"))
	if len(keys) == 0 {
		// Default to every metric the type defines, which is what the detail
		// page wants on first load.
		res, ok := s.store.Resource(id)
		if !ok {
			writeError(w, http.StatusNotFound, "resource_not_found", "No resource with id "+id)
			return
		}
		if t, ok := catalog.Get(res.ResourceType); ok {
			for _, m := range t.Metrics {
				keys = append(keys, m.Key)
			}
		}
	}

	series := make([]model.MetricSeries, 0, len(keys))
	for _, k := range keys {
		ms, err := s.store.Metrics(id, k, from, to, points)
		if err != nil {
			// A single unknown metric key should not fail the whole request.
			s.log.Debug("metric unavailable", "resource", id, "metric", k, "err", err)
			continue
		}
		series = append(series, ms)
	}
	if len(series) == 0 {
		writeError(w, http.StatusNotFound, "no_metrics", "No metric series available for this resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource_id": id, "from": from, "to": to, "series": series,
	})
}

func (s *Server) resourceOutages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.store.Outages(
		r.PathValue("id"), false,
		atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("page_size"), 50)))
}

func (s *Server) listAlarms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AlarmFilter{
		State:      csv(q.Get("state")),
		Severity:   csv(q.Get("severity")),
		Provider:   csv(q.Get("provider")),
		ResourceID: q.Get("resource_id"),
		Query:      q.Get("q"),
		Page:       atoiDefault(q.Get("page"), 1),
		PageSize:   atoiDefault(q.Get("page_size"), 50),
	}
	// Default view is what needs attention: open and acknowledged, not resolved.
	if len(f.State) == 0 && q.Get("all") != "1" {
		f.State = []string{model.AlarmOpen, model.AlarmAcknowledged}
	}
	writeJSON(w, http.StatusOK, s.store.Alarms(f))
}

func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		User string `json:"user"`
	}
	if r.Body != nil {
		// An empty body is fine; only malformed JSON is an error.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_body", "Body must be JSON")
			return
		}
	}
	user := strings.TrimSpace(body.User)
	if user == "" {
		// Real authentication replaces this: the acknowledging user comes from
		// the session, never from the request body. Trusting a client-supplied
		// identity would make the audit trail worthless.
		user = "demo-user"
	}

	alarm, ok := s.store.Acknowledge(id, user)
	if !ok {
		if alarm.ID == "" {
			writeError(w, http.StatusNotFound, "alarm_not_found", "No alarm with id "+id)
			return
		}
		writeError(w, http.StatusConflict, "not_acknowledgeable",
			"Alarm is in state "+alarm.State+" and cannot be acknowledged")
		return
	}
	writeJSON(w, http.StatusOK, alarm)
}

func (s *Server) listOutages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.store.Outages(
		q.Get("resource_id"),
		q.Get("ongoing") == "1" || q.Get("ongoing") == "true",
		atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("page_size"), 50)))
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts := s.store.Accounts()
	writeJSON(w, http.StatusOK, map[string]any{"items": accounts, "total": len(accounts)})
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups := s.store.Groups()
	writeJSON(w, http.StatusOK, map[string]any{"items": groups, "total": len(groups)})
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.DevCORSOrigin != "" {
			// Echo one configured origin rather than "*", so a stray production
			// build of this binary cannot be called from arbitrary sites.
			if r.Header.Get("Origin") == s.cfg.DevCORSOrigin {
				w.Header().Set("Access-Control-Allow-Origin", s.cfg.DevCORSOrigin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if s.cfg.Mock {
			w.Header().Set("X-NimbusEye-Mode", "mock")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	bytes int
}

func (w *statusRecorder) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusRecorder) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method, "path", r.URL.Path, "status", rec.code,
			"bytes", rec.bytes, "dur_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "value", v)
				writeError(w, http.StatusInternalServerError, "internal_error",
					"Unexpected server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, key, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"code": key, "message": msg},
	})
}

func csv(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiDefault(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func modeName(mock bool) string {
	if mock {
		return "mock"
	}
	return "live"
}

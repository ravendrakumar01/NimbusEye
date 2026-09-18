package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"nimbuseye/internal/collect"
	"nimbuseye/internal/model"
)

// registerIngestRoutes wires the collector-facing endpoints.
//
// These are not part of the user-facing API. They are authenticated by a shared
// token rather than a user session, and they fail closed: if no token is
// configured the endpoints are not registered at all, so a default install
// cannot be written to by anyone who can reach the port.
func (s *Server) registerIngestRoutes(mux *http.ServeMux) {
	if s.cfg.IngestToken == "" {
		s.log.Warn("ingest endpoints disabled: no ingest token configured")
		return
	}
	mux.HandleFunc("POST /api/v1/ingest/resources", s.requireIngestToken(s.ingestResources))
	mux.HandleFunc("POST /api/v1/ingest/metrics", s.requireIngestToken(s.ingestMetrics))
	mux.HandleFunc("POST /api/v1/ingest/run", s.requireIngestToken(s.ingestRun))
	s.log.Info("ingest endpoints enabled")
}

func (s *Server) requireIngestToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-NimbusEye-Ingest-Token")
		// Constant-time compare so the token cannot be recovered by timing the
		// responses one byte at a time.
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.IngestToken)) != 1 {
			s.log.Warn("ingest rejected", "path", r.URL.Path, "remote", r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid ingest token")
			return
		}
		next(w, r)
	}
}

// decodeIngest reads a collector payload.
//
// Unknown fields are tolerated here, unlike everywhere else in this API. These
// endpoints are spoken by a sibling binary, and during any deploy the collector
// and the API are briefly at different versions. Being strict turned a new
// optional field into a hard HTTP 400 that silently stopped run reports from
// being recorded — so discovery health went stale while everything looked fine.
//
// Strictness stays where it catches real mistakes: user-supplied request bodies
// and the on-disk account configuration, where an unrecognised key is a typo.
func decodeIngest(r *http.Request, dst any, maxBytes int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBytes))
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) ingestResources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string           `json:"account_id"`
		Resources []model.Resource `json:"resources"`
	}
	// A large tenancy can legitimately produce a few thousand resources.
	if err := decodeIngest(r, &body, 32<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if body.AccountID == "" {
		writeError(w, http.StatusBadRequest, "missing_account", "account_id is required")
		return
	}

	created, updated, err := s.store.UpsertResources(body.AccountID, body.Resources)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ingest_failed", err.Error())
		return
	}
	s.log.Info("resources ingested",
		"account", body.AccountID, "received", len(body.Resources),
		"created", created, "updated", updated)
	writeJSON(w, http.StatusOK, map[string]any{
		"received": len(body.Resources), "created": created, "updated": updated,
	})
}

func (s *Server) ingestMetrics(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Samples []collect.Sample `json:"samples"`
	}
	if err := decodeIngest(r, &body, 32<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	stored, err := s.store.PutSamples(body.Samples)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ingest_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": len(body.Samples), "stored": stored})
}

func (s *Server) ingestRun(w http.ResponseWriter, r *http.Request) {
	var run collect.RunReport
	if err := decodeIngest(r, &run, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := s.store.RecordRun(run); err != nil {
		writeError(w, http.StatusBadRequest, "ingest_failed", err.Error())
		return
	}
	// Logged at warn when degraded: a partial or failed run is the signal that
	// every count in the UI is now suspect.
	if run.State == "ok" {
		s.log.Info("collection run recorded",
			"account", run.AccountID, "state", run.State, "mapped", run.Mapped,
			"took", run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String())
	} else {
		s.log.Warn("collection run degraded",
			"account", run.AccountID, "state", run.State,
			"regions_failed", run.RegionsFailed, "error", run.Error)
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

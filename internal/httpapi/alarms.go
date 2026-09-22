package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Alarm actions.
//
// Acknowledging already existed. These are the three things an operator does with
// an alarm beyond acknowledging it, and each one is deliberately distinct:
//
//	Mute stops the noise and keeps the problem. Used when you know about it, you
//	cannot fix it yet, and you do not want the escalation chain firing at 3am. The
//	alarm stays open and visible; only delivery stops.
//
//	Resolve closes it by hand, with a reason. Needed because not everything the
//	evaluator opens is something it can see the end of — a one-off spike, a
//	condition fixed outside the monitored path. Without this an operator's only
//	options are to wait or to ignore, and ignoring is a habit.
//
//	Root cause records what it turned out to be. The value is entirely in the
//	second occurrence: the same alarm six weeks later, with a note saying what it
//	was last time, is worth more than any dashboard.

func (s *Server) registerAlarmActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/alarms/{id}/mute", s.muteAlarm)
	mux.HandleFunc("POST /api/v1/alarms/{id}/unmute", s.unmuteAlarm)
	mux.HandleFunc("POST /api/v1/alarms/{id}/resolve", s.resolveAlarm)
	mux.HandleFunc("POST /api/v1/alarms/{id}/rca", s.annotateAlarm)
	mux.HandleFunc("GET /api/v1/alarms/{id}/notifications", s.alarmNotifications)
}

// alarmStore is implemented by backends that support alarm actions.
type alarmStore interface {
	MuteAlarm(ctx context.Context, id string, until time.Time, reason, userID string) (model.Alarm, error)
	UnmuteAlarm(ctx context.Context, id, userID string) (model.Alarm, error)
	ResolveAlarm(ctx context.Context, id, reason, userID string) (model.Alarm, error)
	AnnotateAlarm(ctx context.Context, id, note, userID string) (model.Alarm, error)
	AlarmNotifications(ctx context.Context, id string) ([]store.AlertLogEntry, error)
}

func (s *Server) alarms(w http.ResponseWriter) (alarmStore, bool) {
	a, ok := s.store.(alarmStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unavailable",
			"Alarm actions need the database backend.")
		return nil, false
	}
	return a, true
}

// maxMute caps how long an alarm can be silenced.
//
// A week, because an indefinite mute is how a known problem becomes an unknown
// one. If it genuinely needs silencing for longer, the honest instruments are a
// maintenance window or suspending the monitor, and both of those say what they are.
const maxMute = 7 * 24 * time.Hour

func (s *Server) muteAlarm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in struct {
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Minutes <= 0 {
		writeError(w, http.StatusBadRequest, "invalid",
			"Say how long to mute for. A mute with no end is how a known problem becomes a forgotten one.")
		return
	}
	d := time.Duration(in.Minutes) * time.Minute
	if d > maxMute {
		writeError(w, http.StatusBadRequest, "invalid",
			"The longest mute is 7 days. For anything longer, schedule maintenance or suspend the monitor — both of those say what they are.")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeError(w, http.StatusBadRequest, "invalid",
			"Give a reason. Silence without one is indistinguishable from the alerting being broken.")
		return
	}

	al, err := a.MuteAlarm(r.Context(), r.PathValue("id"), time.Now().Add(d),
		strings.TrimSpace(in.Reason), actor)
	if err != nil {
		s.adminError(w, "mute alarm", err)
		return
	}
	s.audit(r, actor, "alarm.mute", "alert", al.ID, map[string]any{
		"minutes": in.Minutes, "reason": in.Reason, "monitor": al.ResourceName,
	})
	writeJSON(w, http.StatusOK, al)
}

func (s *Server) unmuteAlarm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	al, err := a.UnmuteAlarm(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		s.adminError(w, "unmute alarm", err)
		return
	}
	s.audit(r, actor, "alarm.unmute", "alert", al.ID, map[string]any{"monitor": al.ResourceName})
	writeJSON(w, http.StatusOK, al)
}

func (s *Server) resolveAlarm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeError(w, http.StatusBadRequest, "invalid",
			"Give a reason for closing it by hand. The evaluator did not see this condition end, so the record needs to say who decided it had.")
		return
	}
	al, err := a.ResolveAlarm(r.Context(), r.PathValue("id"), strings.TrimSpace(in.Reason), actor)
	if err != nil {
		s.adminError(w, "resolve alarm", err)
		return
	}
	s.audit(r, actor, "alarm.resolve_manual", "alert", al.ID, map[string]any{
		"reason": in.Reason, "monitor": al.ResourceName,
	})
	writeJSON(w, http.StatusOK, al)
}

func (s *Server) annotateAlarm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms(w)
	if !ok {
		return
	}
	actor, ok := s.requireOperator(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		writeError(w, http.StatusBadRequest, "invalid", "The note is empty.")
		return
	}
	if len(note) > 4000 {
		writeError(w, http.StatusBadRequest, "invalid", "Keep the note under 4000 characters.")
		return
	}
	al, err := a.AnnotateAlarm(r.Context(), r.PathValue("id"), note, actor)
	if err != nil {
		s.adminError(w, "annotate alarm", err)
		return
	}
	s.audit(r, actor, "alarm.rca", "alert", al.ID, map[string]any{"monitor": al.ResourceName})
	writeJSON(w, http.StatusOK, al)
}

// alarmNotifications answers "was anyone actually told about this one".
func (s *Server) alarmNotifications(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms(w)
	if !ok {
		return
	}
	entries, err := a.AlarmNotifications(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminError(w, "alarm notifications", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"nimbuseye/internal/store"
)

// Reporting endpoints.
//
// Reports are served only when the backing store can answer them. The in-memory
// backend cannot, and rather than invent availability history the endpoints return
// a clear 501 explaining why. A report is the artefact people forward to other
// people; fabricating one is worse than not having the feature.

// maxReportDays caps the window. The limit is not about query cost — it is that
// availability_daily is the only long-lived source, so a request for two years of
// data would return mostly empty rows and read as data loss rather than as a range
// nobody has collected yet.
const maxReportDays = 400

func (s *Server) registerReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/reports/availability", s.reportAvailability)
	mux.HandleFunc("GET /api/v1/reports/outages", s.reportOutages)
	mux.HandleFunc("GET /api/v1/reports/performance", s.reportPerformance)
	mux.HandleFunc("GET /api/v1/reports/sla", s.reportSLA)
}

// reporter returns the reporting interface, or writes the explanation and reports
// false when the backend cannot produce reports.
func (s *Server) reporter(w http.ResponseWriter) (store.Reporter, bool) {
	r, ok := s.store.(store.Reporter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "reports_unavailable",
			"Reports need the database backend. This instance is running the in-memory "+
				"store, which holds no availability history.")
		return nil, false
	}
	return r, true
}

// parseReportFilter reads the shared query parameters.
//
// The default window is the last 7 days, ending today. Today is included even
// though it is partial: excluding it makes the report look stale to someone who
// just saw an outage on the dashboard.
func parseReportFilter(r *http.Request) (store.ReportFilter, string) {
	q := r.URL.Query()
	loc := time.UTC
	today := time.Now().In(loc).Truncate(24 * time.Hour)

	to := today
	from := today.AddDate(0, 0, -6)

	if v := strings.TrimSpace(q.Get("from")); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return store.ReportFilter{}, "from must be a date in YYYY-MM-DD form"
		}
		from = t
	}
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return store.ReportFilter{}, "to must be a date in YYYY-MM-DD form"
		}
		to = t
	}
	// A reversed range is a mistake, not an empty result: silently returning
	// nothing would look like "no outages" when it means "the dates are swapped".
	if to.Before(from) {
		return store.ReportFilter{}, "to is earlier than from"
	}
	if int(to.Sub(from).Hours()/24)+1 > maxReportDays {
		return store.ReportFilter{}, "the range covers more than " +
			strconv.Itoa(maxReportDays) + " days"
	}

	f := store.ReportFilter{
		Range:        store.Range{From: from, To: to},
		Provider:     splitCSV(q.Get("provider")),
		Type:         splitCSV(q.Get("type")),
		GroupID:      strings.TrimSpace(q.Get("group")),
		Worst:        q.Get("order") == "worst",
		IncludeDaily: q.Get("daily") == "1",
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	return f, ""
}

// splitCSV turns a comma-separated parameter into a slice, dropping blanks so that
// "?provider=" means "no filter" rather than "match the empty provider".
func splitCSV(v string) []string {
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
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) reportAvailability(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reporter(w)
	if !ok {
		return
	}
	f, bad := parseReportFilter(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, "invalid_range", bad)
		return
	}
	out, err := rep.AvailabilityReport(r.Context(), f)
	if err != nil {
		s.log.Error("availability report failed", "err", err)
		writeError(w, http.StatusInternalServerError, "report_failed", "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reportOutages(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reporter(w)
	if !ok {
		return
	}
	f, bad := parseReportFilter(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, "invalid_range", bad)
		return
	}
	out, err := rep.OutageReport(r.Context(), f)
	if err != nil {
		s.log.Error("outage report failed", "err", err)
		writeError(w, http.StatusInternalServerError, "report_failed", "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reportPerformance(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reporter(w)
	if !ok {
		return
	}
	f, bad := parseReportFilter(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, "invalid_range", bad)
		return
	}
	out, err := rep.PerformanceReport(r.Context(), strings.TrimSpace(r.URL.Query().Get("metric")), f)
	if err != nil {
		s.log.Error("performance report failed", "err", err)
		writeError(w, http.StatusInternalServerError, "report_failed", "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reportSLA(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reporter(w)
	if !ok {
		return
	}
	f, bad := parseReportFilter(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, "invalid_range", bad)
		return
	}
	out, err := rep.SLAReport(r.Context(), f)
	if err != nil {
		s.log.Error("sla report failed", "err", err)
		writeError(w, http.StatusInternalServerError, "report_failed", "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Package alert evaluates threshold profiles against collected data and
// maintains alert, outage and availability state.
//
// This is the component that turns an inventory browser into a monitoring tool.
// Everything else in NimbusEye shows what is there; this decides what is wrong.
//
// It talks to PostgreSQL directly rather than through the HTTP API. Evaluation is
// inherently set-based — "which resources have three consecutive samples above
// their threshold" is one query, not two thousand — and routing it through a REST
// interface would mean reimplementing joins in Go.
//
// Design commitments, each of which exists because the obvious alternative
// produces a tool people stop trusting:
//
//   - A rule fires only after polls_check consecutive confirming samples. Cloud
//     metrics arrive late and out of order; alerting on one bad sample means
//     constant false positives.
//   - Alerts are deduplicated on (resource, condition). A flapping resource
//     updates one row rather than creating thousands.
//   - An alert is never opened for a resource inside an active maintenance
//     window; it is recorded as suppressed, so the fact that a condition
//     occurred is not lost.
//   - Recovery is explicit. A condition that stops holding resolves its alert
//     and closes its outage, because an alert nobody clears is an alert nobody
//     reads.
package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
)

// Evaluator holds the database connection and tenant context.
type Evaluator struct {
	pool     *pgxpool.Pool
	tenantID string
	log      *slog.Logger

	// RollupDays is how far back the availability rollup recomputes, in days.
	// One means yesterday and today, which is all that normally changes.
	//
	// It is configurable because the rollup's own definition can change — as it
	// did when unmeasured time stopped being counted as uptime — and every day
	// computed under the old definition then carries a figure the current code
	// would never produce. Leaving those in place means a seven-day report is
	// partly wrong with no indication which part.
	RollupDays int
}

// Report summarises one evaluation pass.
type Report struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Resources  int
	Rules      int
	Opened     int
	Updated    int
	Resolved   int
	Suppressed int
	Escalated  int
	Outages    int
	Closed     int
	RollupDays int
	Notified   int
}

// Rule is one threshold condition, as stored in threshold_profiles.rules.
type Rule struct {
	Metric     string   `json:"metric"`
	Op         string   `json:"op"` // ">=" or "<="
	Trouble    *float64 `json:"trouble"`
	Critical   *float64 `json:"critical"`
	PollsCheck int      `json:"polls_check"`
	Strategy   string   `json:"strategy"`
}

type profile struct {
	id             string
	rules          []Rule
	downPollsCheck int
}

type resourceRow struct {
	id           string
	nativeID     string
	resourceType string
	displayName  string
	status       string
	statusSince  time.Time
	suspended    bool
}

// Open connects and resolves the tenant.
func Open(ctx context.Context, dsn, tenantSlug string, log *slog.Logger) (*Evaluator, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("alert: parse dsn: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("alert: connect: %w", err)
	}
	if tenantSlug == "" {
		tenantSlug = "default"
	}
	var tenantID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM tenant_by_slug($1)`, tenantSlug).
		Scan(&tenantID); err != nil {
		pool.Close()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("alert: no active tenant %q", tenantSlug)
		}
		return nil, fmt.Errorf("alert: resolve tenant: %w", err)
	}
	return &Evaluator{pool: pool, tenantID: tenantID, log: log}, nil
}

// Close releases the pool.
func (e *Evaluator) Close() { e.pool.Close() }

func (e *Evaluator) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('nimbuseye.tenant_id', $1, true)`, e.tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Run performs one full evaluation pass.
func (e *Evaluator) Run(ctx context.Context) (Report, error) {
	rep := Report{StartedAt: time.Now().UTC()}

	err := e.withTx(ctx, func(tx pgx.Tx) error {
		profiles, err := e.loadProfiles(ctx, tx)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			rep.Rules += len(p.rules)
		}

		resources, err := e.loadResources(ctx, tx)
		if err != nil {
			return err
		}
		rep.Resources = len(resources)

		samples, err := e.loadRecentSamples(ctx, tx)
		if err != nil {
			return err
		}

		suppressed, err := e.loadSuppressed(ctx, tx)
		if err != nil {
			return err
		}

		// Conditions this pass believes are true, keyed by dedup key. Anything
		// currently open and absent from this set has recovered.
		firing := map[string]condition{}

		for _, r := range resources {
			// Both the flag and the status are checked. They should agree, and a
			// bug that let them diverge kept 27 intentionally stopped resources
			// alerting; treating either as authoritative removes that class of
			// failure rather than relying on them staying in step.
			if r.suspended || r.status == model.StatusSuspended {
				continue
			}
			p, hasProfile := profiles[r.resourceType]
			t, hasType := catalog.Get(r.resourceType)
			if !hasType {
				continue
			}

			// Availability. The collector decides whether a resource is down;
			// this decides whether it has been down long enough to be believed.
			if t.SupportsAvailability && r.status == model.StatusDown {
				polls := 2
				if hasProfile && p.downPollsCheck > 0 {
					polls = p.downPollsCheck
				}
				needed := time.Duration(polls*t.DefaultPollSec) * time.Second
				if time.Since(r.statusSince) >= needed {
					firing[r.id+":availability"] = condition{
						resourceID: r.id,
						dedupKey:   r.id + ":availability",
						severity:   model.SeverityDown,
						message:    "Resource is not responding to availability checks",
						pollCount:  polls,
						openedAt:   r.statusSince,
					}
				}
			}

			if !hasProfile {
				continue
			}
			for _, rule := range p.rules {
				m, ok := t.Metric(rule.Metric)
				if !ok {
					continue
				}
				recent := samples[r.nativeID+"|"+rule.Metric]
				sev, observed, threshold, ok := evaluate(rule, recent)
				if !ok {
					continue
				}
				key := r.id + ":" + rule.Metric
				firing[key] = condition{
					resourceID:     r.id,
					dedupKey:       key,
					severity:       sev,
					metricKey:      rule.Metric,
					observedValue:  &observed,
					thresholdValue: &threshold,
					message: fmt.Sprintf("%s %s threshold (%s)",
						m.Label, directionWord(rule.Op), formatValue(threshold, m.Unit)),
					pollCount: pollsFor(rule),
					openedAt:  time.Now().UTC(),
				}
			}
		}

		// Apply.
		for key, c := range firing {
			if _, muted := suppressed[c.resourceID]; muted {
				n, err := e.suppress(ctx, tx, c, suppressed[c.resourceID])
				if err != nil {
					return err
				}
				rep.Suppressed += n
				continue
			}
			opened, updated, err := e.upsertAlert(ctx, tx, c)
			if err != nil {
				return fmt.Errorf("upsert alert %s: %w", key, err)
			}
			rep.Opened += opened
			rep.Updated += updated
			if opened > 0 && c.severity == model.SeverityDown {
				n, err := e.openOutage(ctx, tx, c)
				if err != nil {
					return err
				}
				rep.Outages += n
			}
		}

		resolved, closed, err := e.resolveRecovered(ctx, tx, firing)
		if err != nil {
			return err
		}
		rep.Resolved, rep.Closed = resolved, closed

		esc, err := e.escalate(ctx, tx)
		if err != nil {
			return err
		}
		rep.Escalated = esc

		queued, err := e.queueNotifications(ctx, tx)
		if err != nil {
			return err
		}
		rep.Notified = queued

		// Recoveries go to whoever heard about the problem. Queued in the same
		// pass so a resolve and its message cannot end up in different states.
		recovered, err := e.queueRecoveries(ctx, tx)
		if err != nil {
			return fmt.Errorf("queue recoveries: %w", err)
		}
		rep.Notified += recovered

		days, err := e.rollupAvailability(ctx, tx)
		if err != nil {
			return err
		}
		rep.RollupDays = days
		return nil
	})

	rep.FinishedAt = time.Now().UTC()
	return rep, err
}

type condition struct {
	resourceID     string
	dedupKey       string
	severity       string
	metricKey      string
	observedValue  *float64
	thresholdValue *float64
	message        string
	pollCount      int
	openedAt       time.Time
}

/* -------------------------------------------------------------- loading */

func (e *Evaluator) loadProfiles(ctx context.Context, tx pgx.Tx) (map[string]profile, error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text, resource_type, rules, down_polls_check
		 FROM threshold_profiles WHERE is_default`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]profile{}
	for rows.Next() {
		var id, rtype string
		var raw []byte
		var down int
		if err := rows.Scan(&id, &rtype, &raw, &down); err != nil {
			return nil, err
		}
		var rules []Rule
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rules); err != nil {
				// A malformed profile must not take down the whole pass.
				e.log.Warn("threshold profile has unreadable rules",
					"resource_type", rtype, "err", err)
				rules = nil
			}
		}
		out[rtype] = profile{id: id, rules: rules, downPollsCheck: down}
	}
	return out, rows.Err()
}

func (e *Evaluator) loadResources(ctx context.Context, tx pgx.Tx) ([]resourceRow, error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text, native_id, resource_type, display_name, status, status_since, suspended
		 FROM resources WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resourceRow
	for rows.Next() {
		var r resourceRow
		if err := rows.Scan(&r.id, &r.nativeID, &r.resourceType, &r.displayName,
			&r.status, &r.statusSince, &r.suspended); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadRecentSamples fetches the newest samples per series, newest first.
//
// A window function does this in one query. Fetching per resource would be two
// thousand round trips per pass, which is the difference between an evaluator
// that keeps up and one that falls behind.
func (e *Evaluator) loadRecentSamples(ctx context.Context, tx pgx.Tx) (map[string][]float64, error) {
	const maxPolls = 10
	rows, err := tx.Query(ctx,
		`SELECT native_id, metric_key, array_agg(v ORDER BY t DESC) AS recent
		 FROM (
		   SELECT native_id, metric_key, t, v,
		          row_number() OVER (PARTITION BY native_id, metric_key ORDER BY t DESC) AS rn
		   FROM metric_samples
		   -- Only samples recent enough to describe the current state. An old
		   -- series must not keep an alert alive after collection has stopped.
		   WHERE t > now() - interval '2 hours'
		 ) s
		 WHERE rn <= $1
		 GROUP BY native_id, metric_key`, maxPolls)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float64{}
	for rows.Next() {
		var nativeID, metricKey string
		var vals []float64
		if err := rows.Scan(&nativeID, &metricKey, &vals); err != nil {
			return nil, err
		}
		out[nativeID+"|"+metricKey] = vals
	}
	return out, rows.Err()
}

// loadSuppressed returns resource ids covered by an active maintenance window,
// mapped to the window id.
func (e *Evaluator) loadSuppressed(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT r.id::text, w.id::text
		 FROM maintenance_windows w
		 JOIN resources r ON r.deleted_at IS NULL AND (
		      r.id::text = ANY (SELECT jsonb_array_elements_text(coalesce(w.scope->'resource_ids','[]'::jsonb)))
		   OR EXISTS (SELECT 1 FROM resource_group_members m
		              WHERE m.resource_id = r.id
		                AND m.group_id::text = ANY (SELECT jsonb_array_elements_text(
		                    coalesce(w.scope->'group_ids','[]'::jsonb)))))
		 WHERE w.suppress_alerts AND now() BETWEEN w.starts_at AND w.ends_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var resourceID, windowID string
		if err := rows.Scan(&resourceID, &windowID); err != nil {
			return nil, err
		}
		out[resourceID] = windowID
	}
	return out, rows.Err()
}

/* ----------------------------------------------------------- evaluation */

func pollsFor(r Rule) int {
	if r.PollsCheck > 0 {
		return r.PollsCheck
	}
	return 3
}

// evaluate decides whether a rule is breached by the most recent samples.
//
// Returns the severity, the observed value, the threshold crossed, and whether
// anything fired at all. Critical is checked before trouble so a resource sitting
// above both reports the worse of the two.
func evaluate(r Rule, recentNewestFirst []float64) (severity string, observed, threshold float64, fired bool) {
	need := pollsFor(r)
	if len(recentNewestFirst) < need {
		// Not enough confirmation yet. Deliberately silent: a resource that has
		// only just started reporting must not alert on its first sample.
		return "", 0, 0, false
	}
	window := recentNewestFirst[:need]
	observed = window[0]

	breaches := func(limit float64) bool {
		for _, v := range window {
			if r.Op == "<=" {
				if v > limit {
					return false
				}
			} else {
				if v < limit {
					return false
				}
			}
		}
		return true
	}

	if r.Critical != nil && breaches(*r.Critical) {
		return model.SeverityCritical, observed, *r.Critical, true
	}
	if r.Trouble != nil && breaches(*r.Trouble) {
		return model.SeverityTrouble, observed, *r.Trouble, true
	}
	return "", 0, 0, false
}

func directionWord(op string) string {
	if op == "<=" {
		return "below"
	}
	return "above"
}

func formatValue(v float64, unit string) string {
	switch unit {
	case "percent":
		return fmt.Sprintf("%.0f%%", v)
	case "bytes":
		return humanBytes(v)
	case "bytes_per_sec":
		return humanBytes(v) + "/s"
	case "milliseconds":
		return fmt.Sprintf("%.0f ms", v)
	case "seconds":
		return fmt.Sprintf("%.3g s", v)
	default:
		return fmt.Sprintf("%.4g", v)
	}
}

func humanBytes(v float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

/* ------------------------------------------------------- state mutation */

// upsertAlert opens a new alert or refreshes an existing one.
//
// The partial unique index on (tenant_id, dedup_key) WHERE state IN
// ('open','acknowledged') is what makes this safe: at most one live alert per
// condition per resource, so a flapping metric updates one row.
func (e *Evaluator) upsertAlert(ctx context.Context, tx pgx.Tx, c condition) (opened, updated int, err error) {
	// A suppressed alert is the same alert, not a different one. Leaving it out of
	// this lookup meant that when a maintenance window ended, a second row was
	// inserted for a condition that already had one — so the same problem appeared
	// twice in the Alarms list and the suppressed row was never resolved.
	var existingID, existingSeverity, existingState string
	err = tx.QueryRow(ctx,
		`SELECT id::text, severity, state FROM alerts
		 WHERE dedup_key = $1 AND state IN ('open','acknowledged','suppressed')
		   AND resolved_at IS NULL`, c.dedupKey).
		Scan(&existingID, &existingSeverity, &existingState)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx,
			`INSERT INTO alerts (tenant_id, resource_id, dedup_key, severity, state,
			     metric_key, observed_value, threshold_value, message, poll_count, opened_at)
			 VALUES (current_tenant_id(), $1::uuid, $2, $3, 'open', $4, $5, $6, $7, $8, $9)`,
			c.resourceID, c.dedupKey, c.severity, nullIfEmpty(c.metricKey),
			c.observedValue, c.thresholdValue, c.message, c.pollCount, c.openedAt); err != nil {
			return 0, 0, err
		}
		return 1, 0, nil

	case err != nil:
		return 0, 0, err

	default:
		// Refresh the observed value, and let severity escalate. Severity is not
		// lowered on an open alert: an alert that silently downgrades from
		// critical to trouble hides that it was ever critical.
		severity := existingSeverity
		if rank(c.severity) < rank(existingSeverity) {
			severity = c.severity
		}
		// Coming out of a maintenance window: the condition still holds and nothing
		// is suppressing it any more, so the alert becomes live again and the window
		// reference is cleared. Counted as opened, because from an operator's point
		// of view this is the moment it starts demanding attention.
		reopened := existingState == "suppressed"
		if _, err := tx.Exec(ctx,
			`UPDATE alerts SET observed_value = $2, threshold_value = $3, message = $4,
			        severity = $5, poll_count = poll_count + 1, updated_at = now(),
			        state = CASE WHEN state = 'suppressed' THEN 'open' ELSE state END,
			        suppressed_by_maintenance = NULL
			 WHERE id = $1::uuid`,
			existingID, c.observedValue, c.thresholdValue, c.message, severity); err != nil {
			return 0, 0, err
		}
		if reopened {
			return 1, 0, nil
		}
		return 0, 1, nil
	}
}

func rank(sev string) int {
	switch sev {
	case model.SeverityDown:
		return 0
	case model.SeverityCritical:
		return 1
	case model.SeverityTrouble:
		return 2
	default:
		return 3
	}
}

// suppress records a condition that occurred inside a maintenance window.
//
// The row is written rather than skipped so post-maintenance review can see what
// would have fired; it just never notifies anyone.
func (e *Evaluator) suppress(ctx context.Context, tx pgx.Tx, c condition, windowID string) (int, error) {
	// An alert that was already open when the window started is the same alert.
	// Checking only for an existing *suppressed* row meant a second row was
	// inserted beside the open one, so during maintenance the same problem was
	// listed twice — once notifying and once not.
	var existingID, existingState string
	err := tx.QueryRow(ctx,
		`SELECT id::text, state FROM alerts
		 WHERE dedup_key = $1 AND state IN ('open','acknowledged','suppressed')
		   AND resolved_at IS NULL`, c.dedupKey).Scan(&existingID, &existingState)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx,
			`UPDATE alerts SET state = 'suppressed', suppressed_by_maintenance = $2::uuid,
			        observed_value = $3, threshold_value = $4, message = $5,
			        poll_count = poll_count + 1, updated_at = now()
			 WHERE id = $1::uuid`,
			existingID, windowID, c.observedValue, c.thresholdValue, c.message); err != nil {
			return 0, err
		}
		// Only count it as newly suppressed the first time, so a long window does
		// not inflate the figure on every pass.
		if existingState == "suppressed" {
			return 0, nil
		}
		return 1, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO alerts (tenant_id, resource_id, dedup_key, severity, state,
		     metric_key, observed_value, threshold_value, message, poll_count,
		     opened_at, suppressed_by_maintenance)
		 VALUES (current_tenant_id(), $1::uuid, $2, $3, 'suppressed', $4, $5, $6, $7, $8, $9, $10::uuid)`,
		c.resourceID, c.dedupKey, c.severity, nullIfEmpty(c.metricKey),
		c.observedValue, c.thresholdValue, c.message, c.pollCount, c.openedAt, windowID); err != nil {
		return 0, err
	}
	return 1, nil
}

// openOutage records the start of an availability interval.
func (e *Evaluator) openOutage(ctx context.Context, tx pgx.Tx, c condition) (int, error) {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM outages WHERE resource_id = $1::uuid AND ended_at IS NULL)`,
		c.resourceID).Scan(&exists); err != nil {
		return 0, err
	}
	if exists {
		return 0, nil
	}
	// started_at is the moment the resource entered its current state, not the
	// moment the evaluator noticed. Otherwise every outage under-reports by the
	// confirmation delay.
	if _, err := tx.Exec(ctx,
		`INSERT INTO outages (tenant_id, resource_id, alert_id, started_at, severity, classified_as)
		 VALUES (current_tenant_id(), $1::uuid,
		         (SELECT id FROM alerts WHERE dedup_key = $2 AND state IN ('open','acknowledged')),
		         $3, 'down', 'outage')`,
		c.resourceID, c.dedupKey, c.openedAt); err != nil {
		return 0, err
	}
	return 1, nil
}

// resolveRecovered closes alerts whose condition no longer holds.
func (e *Evaluator) resolveRecovered(ctx context.Context, tx pgx.Tx, firing map[string]condition) (resolved, closed int, err error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text, dedup_key, resource_id::text, severity FROM alerts
		 -- Suppressed counts as live. Excluding it left an alert stuck in
		 -- suppression for good once its condition cleared during a window.
		 WHERE state IN ('open','acknowledged','suppressed') AND resolved_at IS NULL`)
	if err != nil {
		return 0, 0, err
	}
	type live struct{ id, key, resourceID, severity string }
	var all []live
	for rows.Next() {
		var l live
		if err := rows.Scan(&l.id, &l.key, &l.resourceID, &l.severity); err != nil {
			rows.Close()
			return 0, 0, err
		}
		all = append(all, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, l := range all {
		if _, still := firing[l.key]; still {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE alerts SET state = 'resolved', resolved_at = now(), updated_at = now()
			 WHERE id = $1::uuid`, l.id); err != nil {
			return resolved, closed, err
		}
		resolved++

		if l.severity == model.SeverityDown {
			tag, err := tx.Exec(ctx,
				`UPDATE outages
				 SET ended_at = now(),
				     duration_sec = extract(epoch FROM (now() - started_at))::int
				 WHERE resource_id = $1::uuid AND ended_at IS NULL`, l.resourceID)
			if err != nil {
				return resolved, closed, err
			}
			closed += int(tag.RowsAffected())
		}
	}
	return resolved, closed, nil
}

// escalate advances the escalation level of alerts nobody has acknowledged.
//
// The evaluated reference account had no escalation configured at all, which
// means a real outage would have notified once and then gone quiet. Levels here
// are time-based and deliberately simple: 15 minutes to level 1, an hour to
// level 2.
func (e *Evaluator) escalate(ctx context.Context, tx pgx.Tx) (int, error) {
	tag, err := tx.Exec(ctx,
		`UPDATE alerts SET escalation_level = CASE
		     WHEN now() - opened_at > interval '1 hour'    AND escalation_level < 2 THEN 2
		     WHEN now() - opened_at > interval '15 minutes' AND escalation_level < 1 THEN 1
		     ELSE escalation_level END,
		     updated_at = now()
		 WHERE state = 'open'
		   AND (
		     (now() - opened_at > interval '15 minutes' AND escalation_level < 1) OR
		     (now() - opened_at > interval '1 hour'     AND escalation_level < 2))`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// rollupAvailability recomputes availability_daily over the configured window.
//
// Derived from closed and ongoing outages rather than from raw status history,
// which is not retained. Reports read this table, so an SLA figure and an outage
// list can never disagree.
//
// The hard part is not downtime, it is time that was never measured. The obvious
// formula — up = elapsed - down — silently credits full uptime to a resource that
// has never returned a single metric, because a resource nobody is measuring has
// no outages. That produces a report claiming 100% availability for resources the
// tool is not actually watching, which is worse than reporting nothing: it is a
// confident false negative.
//
// So each day is attributed to one of four buckets, and availability is left NULL
// unless the day was genuinely measured:
//
//	measured and working     -> up_sec
//	measured and failing     -> down_sec        (from the outages table)
//	intentionally stopped    -> maintenance_sec (not an outage, not uptime)
//	never measured           -> unknown_sec     (availability_pct stays NULL)
//
// Evidence of measurement is a metric sample in that day, or an outage — an
// outage is itself proof the resource was being watched, which matters because a
// failing check produces no sample.
func (e *Evaluator) rollupAvailability(ctx context.Context, tx pgx.Tx) (int, error) {
	tag, err := tx.Exec(ctx,
		`WITH days AS (
		   SELECT d::date AS day
		     FROM generate_series(current_date - $1::int, current_date, '1 day') d
		 ),
		 spans AS (
		   SELECT r.id AS resource_id, dy.day,
		          -- Seconds of overlap between each outage and the day, clamped so
		          -- an outage spanning midnight is split correctly across days.
		          --
		          -- The CASE is essential, not defensive. PostgreSQL GREATEST and
		          -- LEAST ignore NULL arguments rather than returning NULL, so on a
		          -- LEFT JOIN row with no outage the GREATEST() collapses to the day
		          -- boundary and the expression measures the entire day as downtime.
		          -- Without this guard every healthy resource reports 0 percent
		          -- availability, the exact inverse of the truth.
		          coalesce(sum(
		            CASE WHEN o.id IS NULL THEN 0
		                 ELSE GREATEST(0, extract(epoch FROM (
		                        LEAST(coalesce(o.ended_at, now()), (dy.day + 1)::timestamptz)
		                      - GREATEST(o.started_at, dy.day::timestamptz))))::int
		            END)::int, 0) AS down_sec,
		          count(o.id) AS outage_count
		   FROM resources r
		   CROSS JOIN days dy
		   LEFT JOIN outages o
		          ON o.resource_id = r.id
		         AND o.classified_as = 'outage'
		         AND o.started_at < (dy.day + 1)::timestamptz
		         AND coalesce(o.ended_at, now()) > dy.day::timestamptz
		   WHERE r.deleted_at IS NULL
		     -- A resource has no availability for a day that predates it. Without
		     -- this, widening the window invents rows and a report then claims more
		     -- measured days than the resource has existed for.
		     AND dy.day >= r.created_at::date
		   GROUP BY r.id, dy.day
		 ),
		 observed AS (
		   -- Did anything actually measure this resource on this day? Joined on
		   -- native_id because that is what the sample carries.
		   SELECT r.id AS resource_id, dy.day,
		          count(m.native_id) AS samples
		   FROM resources r
		   CROSS JOIN days dy
		   LEFT JOIN metric_samples m
		          ON m.native_id = r.native_id
		         AND m.t >= dy.day::timestamptz
		         AND m.t <  (dy.day + 1)::timestamptz
		   WHERE r.deleted_at IS NULL
		     AND dy.day >= r.created_at::date
		   GROUP BY r.id, dy.day
		 )
		 INSERT INTO availability_daily
		   (tenant_id, resource_id, day, up_sec, down_sec, maintenance_sec, unknown_sec,
		    availability_pct, outage_count, mttr_sec)
		 SELECT current_tenant_id(), s.resource_id, s.day,
		        CASE WHEN st.excluded OR NOT st.measured THEN 0
		             ELSE GREATEST(0, elapsed.sec - s.down_sec) END,
		        CASE WHEN st.excluded THEN 0 ELSE s.down_sec END,
		        CASE WHEN st.excluded THEN elapsed.sec ELSE 0 END,
		        CASE WHEN NOT st.excluded AND NOT st.measured THEN elapsed.sec ELSE 0 END,
		        -- NULL rather than a number whenever the day cannot honestly be
		        -- scored. Reports render NULL as an em dash, so an unmeasured day
		        -- reads as "no data" instead of as a perfect score.
		        CASE WHEN st.excluded OR NOT st.measured THEN NULL
		             WHEN elapsed.sec > 0
		             THEN round(100.0 * GREATEST(0, elapsed.sec - s.down_sec) / elapsed.sec, 3)
		             ELSE NULL END,
		        CASE WHEN st.excluded THEN 0 ELSE s.outage_count END,
		        CASE WHEN NOT st.excluded AND s.outage_count > 0
		             THEN s.down_sec / s.outage_count ELSE NULL END
		 FROM spans s
		 JOIN observed ob ON ob.resource_id = s.resource_id AND ob.day = s.day
		 JOIN resources r ON r.id = s.resource_id
		 CROSS JOIN LATERAL (
		   -- Today is only partly elapsed; dividing by a full day would understate
		   -- availability for every resource every morning.
		   SELECT LEAST(86400, GREATEST(1, extract(epoch FROM (
		            LEAST(now(), (s.day + 1)::timestamptz) - s.day::timestamptz))::int)) AS sec
		 ) elapsed
		 CROSS JOIN LATERAL (
		   SELECT
		     -- Suspended means deliberately stopped. Counting that as downtime
		     -- would bury real outages under planned ones; counting it as uptime
		     -- would claim a stopped machine was serving traffic.
		     --
		     -- Only the current flag is available, not a history of it, so
		     -- suspending a resource reclassifies its last two days. The rollup
		     -- window is two days, which bounds that to the same period.
		     (r.suspended OR r.status = 'suspended') AS excluded,
		     (ob.samples > 0 OR s.down_sec > 0)      AS measured
		 ) st
		 ON CONFLICT (tenant_id, resource_id, day) DO UPDATE SET
		   up_sec = EXCLUDED.up_sec, down_sec = EXCLUDED.down_sec,
		   maintenance_sec = EXCLUDED.maintenance_sec,
		   unknown_sec = EXCLUDED.unknown_sec,
		   availability_pct = EXCLUDED.availability_pct,
		   outage_count = EXCLUDED.outage_count, mttr_sec = EXCLUDED.mttr_sec`,
		e.rollupWindow())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// rollupWindow clamps the configured window to something sane. Zero means the
// field was never set, which is the normal service path.
func (e *Evaluator) rollupWindow() int {
	if e.RollupDays <= 0 {
		return 1
	}
	if e.RollupDays > 400 {
		return 400
	}
	return e.RollupDays
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

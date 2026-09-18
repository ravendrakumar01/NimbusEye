package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/store"
)

// Reporting queries.
//
// Everything here reads the same tables the dashboard reads — availability_daily,
// outages, metric_samples — so a report and the live view cannot disagree. That is
// the point of having the alerter maintain a daily rollup rather than recomputing
// from raw samples at report time.

// Compile-time assertion that this backend can report.
var _ store.Reporter = (*Store)(nil)

// reportScope builds the predicate shared by every report, so a filter means the
// same thing in all of them.
func (s *Store) reportScope(f store.ReportFilter, args *[]any) string {
	conds := []string{"r.deleted_at IS NULL"}
	next := func(v any) string {
		*args = append(*args, v)
		return fmt.Sprintf("$%d", len(*args))
	}
	if len(f.Type) > 0 {
		conds = append(conds, "r.resource_type = ANY("+next(f.Type)+")")
	}
	if codes := codesFor(f.Provider, nil); codes != nil {
		conds = append(conds, "r.resource_type = ANY("+next(codes)+")")
	}
	if f.GroupID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM resource_group_members m "+
			"WHERE m.resource_id = r.id AND m.group_id = "+next(f.GroupID)+"::uuid)")
	}
	return strings.Join(conds, " AND ")
}

func limitOr(n, def int) int {
	if n <= 0 {
		return def
	}
	if n > 500 {
		return 500
	}
	return n
}

// AvailabilityReport summarises uptime over the window.
func (s *Store) AvailabilityReport(ctx context.Context, f store.ReportFilter) (store.AvailabilitySummary, error) {
	out := store.AvailabilitySummary{
		From:       f.Range.From.Format("2006-01-02"),
		To:         f.Range.To.Format("2006-01-02"),
		ByProvider: map[string]float64{},
		ByType:     map[string]float64{},
		Rows:       []store.AvailabilityRow{},
	}

	args := []any{f.Range.From, f.Range.To}
	scope := s.reportScope(f, &args)
	// Ordering applies to the CTE's output columns, not to the joined tables,
	// which are out of scope by the time the outer select runs.
	order := "display_name ASC"
	if f.Worst {
		// Nulls last: a resource with no data is not the worst performer, it is an
		// unmeasured one, and putting it at the top of a "worst" list is misleading.
		order = "avail ASC NULLS LAST"
	}
	args = append(args, limitOr(f.Limit, 200))

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH agg AS (
			  SELECT r.id, r.display_name, r.resource_type, coalesce(r.region,'') AS region,
			         coalesce(sum(d.up_sec),0)::int          AS up_sec,
			         coalesce(sum(d.down_sec),0)::int        AS down_sec,
			         coalesce(sum(d.maintenance_sec),0)::int AS maintenance_sec,
			         -- Mean of the daily figures rather than up/(up+down) across the
			         -- window: a resource monitored for two of seven days must not be
			         -- reported as if the other five were perfect.
			         avg(d.availability_pct)                 AS avail,
			         coalesce(sum(d.outage_count),0)::int    AS outages,
			         avg(d.mttr_sec)                         AS mttr,
			         count(d.availability_pct)::int          AS days_with_data
			  FROM resources r
			  LEFT JOIN availability_daily d
			         ON d.resource_id = r.id AND d.day BETWEEN $1::date AND $2::date
			  WHERE `+scope+`
			  GROUP BY r.id, r.display_name, r.resource_type, r.region
			)
			SELECT id::text, display_name, resource_type, region, up_sec, down_sec,
			       maintenance_sec, avail, outages, mttr, days_with_data
			FROM agg ORDER BY `+order+` LIMIT $`+fmt.Sprint(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row store.AvailabilityRow
			var mttr *float64
			if err := rows.Scan(&row.ResourceID, &row.DisplayName, &row.ResourceType,
				&row.Region, &row.UpSec, &row.DownSec, &row.MaintenanceSec,
				&row.AvailabilityPct, &row.OutageCount, &mttr, &row.DaysWithData); err != nil {
				return err
			}
			if t, ok := catalog.Get(row.ResourceType); ok {
				row.Provider, row.TypeName = t.Provider, t.DisplayName
			}
			if mttr != nil {
				v := int(*mttr)
				row.MTTRSec = &v
			}
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}

	// Aggregates are computed over the whole scope, not the returned page, so a
	// limited "worst 20" view still reports the true estate-wide figures.
	aggArgs := []any{f.Range.From, f.Range.To}
	aggScope := s.reportScope(f, &aggArgs)
	s.read(ctx, "availability aggregates", func(tx pgx.Tx) error {
		var mean *float64
		if err := tx.QueryRow(ctx, `
			SELECT count(DISTINCT r.id)::int,
			       avg(d.availability_pct),
			       coalesce(sum(d.down_sec),0)::int,
			       coalesce(sum(d.outage_count),0)::int
			FROM resources r
			LEFT JOIN availability_daily d
			       ON d.resource_id = r.id AND d.day BETWEEN $1::date AND $2::date
			WHERE `+aggScope, aggArgs...).
			Scan(&out.Resources, &mean, &out.TotalDownSec, &out.TotalOutages); err != nil {
			return err
		}
		out.MeanAvailability = round3p(mean)

		byType, err := tx.Query(ctx, `
			SELECT r.resource_type, avg(d.availability_pct)
			FROM resources r
			JOIN availability_daily d
			  ON d.resource_id = r.id AND d.day BETWEEN $1::date AND $2::date
			WHERE `+aggScope+`
			GROUP BY r.resource_type`, aggArgs...)
		if err != nil {
			return err
		}
		defer byType.Close()
		// Provider figures are the mean of their types' means, which is what a
		// per-provider headline should be at this granularity.
		provSum := map[string]float64{}
		provN := map[string]int{}
		for byType.Next() {
			var code string
			var avg *float64
			if err := byType.Scan(&code, &avg); err != nil {
				return err
			}
			if avg == nil {
				continue
			}
			t, ok := catalog.Get(code)
			if !ok {
				continue
			}
			out.ByType[t.DisplayName] = round3(*avg)
			provSum[t.Provider] += *avg
			provN[t.Provider]++
		}
		for p, sum := range provSum {
			out.ByProvider[p] = round3(sum / float64(provN[p]))
		}
		return byType.Err()
	})

	if f.IncludeDaily && len(out.Rows) > 0 {
		s.attachDaily(ctx, out.Rows, f.Range)
	}
	return out, nil
}

// attachDaily fills the per-day series for a trend line, in one query for the whole
// page rather than one per row.
func (s *Store) attachDaily(ctx context.Context, rows []store.AvailabilityRow, rng store.Range) {
	ids := make([]string, 0, len(rows))
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		ids = append(ids, r.ResourceID)
		idx[r.ResourceID] = i
	}
	s.read(ctx, "availability daily", func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `
			SELECT resource_id::text, day::text, availability_pct, down_sec
			FROM availability_daily
			WHERE resource_id = ANY($1::uuid[]) AND day BETWEEN $2::date AND $3::date
			ORDER BY day ASC`, ids, rng.From, rng.To)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var id, day string
			var pct *float64
			var down int
			if err := q.Scan(&id, &day, &pct, &down); err != nil {
				return err
			}
			if i, ok := idx[id]; ok {
				rows[i].Daily = append(rows[i].Daily,
					store.DailyPoint{Day: day, AvailabilityPct: round3p(pct), DownSec: down})
			}
		}
		return q.Err()
	})
}

// OutageReport lists outages in the window with their aggregates.
func (s *Store) OutageReport(ctx context.Context, f store.ReportFilter) (store.OutageReport, error) {
	out := store.OutageReport{
		From: f.Range.From.Format("2006-01-02"),
		To:   f.Range.To.Format("2006-01-02"),
		Rows: []store.OutageRow{},
	}

	args := []any{f.Range.From, f.Range.To}
	scope := s.reportScope(f, &args)
	// An outage overlapping the window counts, not only one contained by it —
	// otherwise a three-day outage vanishes from a one-day report.
	overlap := `o.started_at < ($2::date + 1) AND coalesce(o.ended_at, now()) >= $1::date`
	args = append(args, limitOr(f.Limit, 200))

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int,
			       count(*) FILTER (WHERE o.ended_at IS NULL)::int,
			       coalesce(sum(coalesce(o.duration_sec,
			           extract(epoch FROM (now() - o.started_at))::int)),0)::int,
			       avg(o.duration_sec) FILTER (WHERE o.ended_at IS NOT NULL),
			       coalesce(max(coalesce(o.duration_sec,
			           extract(epoch FROM (now() - o.started_at))::int)),0)::int
			FROM outages o JOIN resources r ON r.id = o.resource_id
			WHERE `+scope+` AND `+overlap+` AND o.classified_as = 'outage'`,
			args[:len(args)-1]...).
			Scan(&out.Total, &out.Ongoing, &out.TotalDownSec, &out.MeanMTTRSec, &out.LongestSec); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.resource_id::text, r.display_name, r.resource_type,
			       coalesce(r.region,''), o.started_at, o.ended_at,
			       coalesce(o.duration_sec, extract(epoch FROM (now() - o.started_at))::int),
			       o.severity, o.classified_as, coalesce(o.root_cause,'')
			FROM outages o JOIN resources r ON r.id = o.resource_id
			WHERE `+scope+` AND `+overlap+`
			ORDER BY o.started_at DESC LIMIT $`+fmt.Sprint(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row store.OutageRow
			var code string
			if err := rows.Scan(&row.ID, &row.ResourceID, &row.DisplayName, &code,
				&row.Region, &row.StartedAt, &row.EndedAt, &row.DurationSec,
				&row.Severity, &row.ClassifiedAs, &row.RootCause); err != nil {
				return err
			}
			if t, ok := catalog.Get(code); ok {
				row.Provider, row.TypeName = t.Provider, t.DisplayName
			}
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	return out, err
}

// PerformanceReport summarises one metric across the scope.
//
// When metricKey is empty it reports whichever metric has the most data, so the
// page has something meaningful on first load rather than an empty selector.
func (s *Store) PerformanceReport(ctx context.Context, metricKey string, f store.ReportFilter) (store.PerformanceReport, error) {
	out := store.PerformanceReport{
		From: f.Range.From.Format("2006-01-02"),
		To:   f.Range.To.Format("2006-01-02"),
		Rows: []store.PerformanceRow{}, AvailableMetrics: []store.MetricOption{},
	}

	optArgs := []any{f.Range.From, f.Range.To}
	optScope := s.reportScope(f, &optArgs)
	s.read(ctx, "performance metric options", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT m.metric_key, count(DISTINCT r.id)::int
			FROM metric_samples m
			JOIN resources r ON r.native_id = m.native_id
			WHERE `+optScope+` AND m.t >= $1::date AND m.t < ($2::date + 1)
			GROUP BY m.metric_key ORDER BY 2 DESC, 1 ASC`, optArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var n int
			if err := rows.Scan(&key, &n); err != nil {
				return err
			}
			opt := store.MetricOption{Key: key, Label: key, Count: n}
			// Label and unit come from the catalog, so the report names a metric the
			// same way every other screen does.
			for _, t := range catalog.All() {
				if m, ok := t.Metric(key); ok {
					opt.Label, opt.Unit = m.Label, m.Unit
					break
				}
			}
			out.AvailableMetrics = append(out.AvailableMetrics, opt)
		}
		return rows.Err()
	})

	if metricKey == "" {
		if len(out.AvailableMetrics) == 0 {
			return out, nil
		}
		metricKey = out.AvailableMetrics[0].Key
	}
	out.MetricKey = metricKey

	args := []any{f.Range.From, f.Range.To, metricKey}
	scope := s.reportScope(f, &args)
	order := "avg_v DESC"
	if f.Worst {
		order = "p95 DESC"
	}
	args = append(args, limitOr(f.Limit, 100))

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.display_name, r.resource_type,
			       avg(m.v) AS avg_v, min(m.v), max(m.v),
			       percentile_cont(0.95) WITHIN GROUP (ORDER BY m.v) AS p95,
			       count(*)::int
			FROM metric_samples m
			JOIN resources r ON r.native_id = m.native_id
			WHERE `+scope+` AND m.metric_key = $3
			  AND m.t >= $1::date AND m.t < ($2::date + 1)
			GROUP BY r.id, r.display_name, r.resource_type
			ORDER BY `+order+` LIMIT $`+fmt.Sprint(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row store.PerformanceRow
			var code string
			if err := rows.Scan(&row.ResourceID, &row.DisplayName, &code,
				&row.Avg, &row.Min, &row.Max, &row.P95, &row.Samples); err != nil {
				return err
			}
			row.MetricKey = metricKey
			if t, ok := catalog.Get(code); ok {
				row.Provider, row.TypeName = t.Provider, t.DisplayName
				if m, ok := t.Metric(metricKey); ok {
					row.Label, row.Unit = m.Label, m.Unit
					row.Trouble, row.Critical = m.Trouble, m.Critical
					// Judged on P95, not the maximum: a single spike is not a
					// sustained problem, and the maximum would flag everything.
					row.Breaching = breachLevel(row.P95, m.Trouble, m.Critical, m.HigherIsWorse)
				}
			}
			row.Avg, row.Min, row.Max, row.P95 =
				round3(row.Avg), round3(row.Min), round3(row.Max), round3(row.P95)
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	return out, err
}

func breachLevel(v float64, trouble, critical *float64, higherIsWorse bool) string {
	worse := func(a, b float64) bool {
		if higherIsWorse {
			return a >= b
		}
		return a <= b
	}
	if critical != nil && worse(v, *critical) {
		return "critical"
	}
	if trouble != nil && worse(v, *trouble) {
		return "trouble"
	}
	return ""
}

// SLAReport measures each SLA definition against actual availability.
func (s *Store) SLAReport(ctx context.Context, f store.ReportFilter) (store.SLAReport, error) {
	out := store.SLAReport{
		From: f.Range.From.Format("2006-01-02"),
		To:   f.Range.To.Format("2006-01-02"),
		Rows: []store.SLARow{},
	}

	type def struct {
		id, name, period string
		target           float64
		scope            map[string]any
	}
	var defs []def

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, display_name, target_pct, period, scope
			 FROM sla_definitions ORDER BY display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d def
			if err := rows.Scan(&d.id, &d.name, &d.target, &d.period, &d.scope); err != nil {
				return err
			}
			defs = append(defs, d)
		}
		return rows.Err()
	})
	if err != nil || len(defs) == 0 {
		return out, err
	}

	for _, d := range defs {
		row := store.SLARow{
			ID: d.id, DisplayName: d.name, TargetPct: d.target, Period: d.period,
		}
		// The scope shape mirrors maintenance windows: resource_ids and group_ids.
		var ids []string
		if raw, ok := d.scope["resource_ids"].([]any); ok {
			for _, v := range raw {
				if sv, ok := v.(string); ok {
					ids = append(ids, sv)
				}
			}
		}
		var groupID string
		if raw, ok := d.scope["group_ids"].([]any); ok && len(raw) > 0 {
			if sv, ok := raw[0].(string); ok {
				groupID = sv
			}
		}

		s.read(ctx, "sla row", func(tx pgx.Tx) error {
			conds := []string{"r.deleted_at IS NULL"}
			args := []any{f.Range.From, f.Range.To}
			if len(ids) > 0 {
				args = append(args, ids)
				conds = append(conds, fmt.Sprintf("r.id = ANY($%d::uuid[])", len(args)))
			}
			if groupID != "" {
				args = append(args, groupID)
				conds = append(conds, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM resource_group_members m WHERE m.resource_id = r.id AND m.group_id = $%d::uuid)", len(args)))
			}
			var actual *float64
			if err := tx.QueryRow(ctx, `
				SELECT count(DISTINCT r.id)::int,
				       avg(d.availability_pct),
				       coalesce(sum(d.down_sec),0)::int
				FROM resources r
				LEFT JOIN availability_daily d
				       ON d.resource_id = r.id AND d.day BETWEEN $1::date AND $2::date
				WHERE `+strings.Join(conds, " AND "), args...).
				Scan(&row.ResourceCount, &actual, &row.DownSec); err != nil {
				return err
			}
			row.ActualPct = round3p(actual)
			// Compliance is only stated when there is something to state. An SLA
			// with no measured days is not "breached", it is unmeasured, and
			// reporting a red cross for it would be a false accusation.
			if actual != nil {
				ok := *actual >= d.target
				row.Compliant = &ok
			}
			return nil
		})

		// Error budget: the downtime the target still permits over the window.
		// Expressed in seconds because that is actionable, unlike a percentage
		// someone has to convert.
		budget := int(float64(f.Range.Days()) * 86400 * (100 - d.target) / 100)
		remaining := budget - row.DownSec
		row.ErrorBudgetSec = &remaining
		out.Rows = append(out.Rows, row)
	}

	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].DisplayName < out.Rows[j].DisplayName })
	return out, nil
}

func round3p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round3(*v)
	return &r
}

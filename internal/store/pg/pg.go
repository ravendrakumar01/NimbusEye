// Package pg implements the storage contract against PostgreSQL.
//
// Every query runs inside a transaction that first sets `nimbuseye.tenant_id`,
// which is what the row-level security policies read. That is not belt-and-braces
// on top of a WHERE clause — it is the isolation mechanism. A handler that
// forgets a tenant filter returns nothing here instead of leaking across
// tenants, and the application role is deliberately neither the schema owner nor
// a superuser so the policies cannot be bypassed.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/collect"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Store is the PostgreSQL-backed implementation.
type Store struct {
	pool     *pgxpool.Pool
	tenantID string
	log      *slog.Logger
}

// Compile-time assertion that this backend implements the full contract.
var _ store.Store = (*Store)(nil)

// Open connects, resolves the tenant, and verifies the schema is present.
func Open(ctx context.Context, dsn, tenantSlug string, log *slog.Logger) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	// Sized for a single API process serving a dashboard: enough for concurrent
	// page loads, small enough that a runaway query cannot exhaust the server's
	// connection slots.
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}

	if tenantSlug == "" {
		tenantSlug = "default"
	}
	// Resolved through a SECURITY DEFINER function, because the policy on
	// `tenants` is `id = current_tenant_id()` and the setting is not established
	// yet — the app role cannot see the tenant it is about to work as. See
	// db/migrations/008_tenant_lookup.sql.
	var tenantID string
	err = pool.QueryRow(ctx,
		`SELECT id::text FROM tenant_by_slug($1)`, tenantSlug).Scan(&tenantID)
	if err != nil {
		pool.Close()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("pg: no active tenant with slug %q; run --migrate to provision the default tenant", tenantSlug)
		}
		return nil, fmt.Errorf("pg: resolve tenant: %w", err)
	}

	s := &Store{pool: pool, tenantID: tenantID, log: log}
	log.Info("postgres store ready", "tenant", tenantSlug, "tenant_id", tenantID)
	return s, nil
}

// Pool exposes the connection pool.
//
// Needed by the auth package, whose two pre-authentication queries must run
// outside a tenant context and therefore cannot go through this store's
// tenant-scoped transaction helper.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close(context.Context) error {
	s.pool.Close()
	return nil
}

// withTx runs fn inside a transaction whose tenant context is set.
//
// set_config with is_local = true scopes the setting to this transaction, so a
// pooled connection cannot carry one request's tenant into the next.
func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT set_config('nimbuseye.tenant_id', $1, true)`, s.tenantID); err != nil {
		return fmt.Errorf("pg: set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// read is withTx for queries, logging rather than propagating errors where the
// contract has no error return.
func (s *Store) read(ctx context.Context, what string, fn func(pgx.Tx) error) {
	if err := s.withTx(ctx, fn); err != nil {
		s.log.Error("query failed", "what", what, "err", err)
	}
}

/* ------------------------------------------------------------- resources */

// resourceColumns is the shared projection, so every scan target matches.
const resourceColumns = `
	r.id::text, r.cloud_account_id::text, r.resource_type, r.native_id, r.display_name,
	coalesce(r.region,''), coalesce(r.availability_zone,''), r.status, r.status_since,
	r.last_polled_at, r.suspended, r.attributes, r.discovered_at, r.last_seen_at`

func scanResource(rows pgx.Rows) (model.Resource, error) {
	var r model.Resource
	var accountID *string
	if err := rows.Scan(
		&r.ID, &accountID, &r.ResourceType, &r.NativeID, &r.DisplayName,
		&r.Region, &r.AvailabilityZone, &r.Status, &r.StatusSince,
		&r.LastPolledAt, &r.Suspended, &r.Attributes, &r.DiscoveredAt, &r.LastSeenAt,
	); err != nil {
		return r, err
	}
	if accountID != nil {
		r.CloudAccountID = *accountID
	}
	// Presentation metadata comes from the embedded catalog rather than a join:
	// it is static, and joining resource_types on every row buys nothing.
	if t, ok := catalog.Get(r.ResourceType); ok {
		r.Provider = t.Provider
		r.TypeName = t.DisplayName
		r.Category = t.Category
	}
	if r.Attributes == nil {
		r.Attributes = map[string]any{}
	}
	if r.Tags == nil {
		r.Tags = map[string]string{}
	}
	return r, nil
}

// resourceWhere builds the shared predicate for list and count queries, so the
// status rings can never disagree with the rows beneath them.
func resourceWhere(f store.ResourceFilter) (string, []any) {
	var conds []string
	var args []any

	conds = append(conds, "r.deleted_at IS NULL")

	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if q := strings.TrimSpace(f.Query); q != "" {
		p := next("%" + strings.ToLower(q) + "%")
		conds = append(conds, fmt.Sprintf(
			"(lower(r.display_name) LIKE %s OR lower(r.native_id) LIKE %s)", p, p))
	}
	if len(f.Status) > 0 {
		conds = append(conds, "r.status = ANY("+next(f.Status)+")")
	}
	if len(f.Type) > 0 {
		conds = append(conds, "r.resource_type = ANY("+next(f.Type)+")")
	}
	if len(f.Region) > 0 {
		conds = append(conds, "r.region = ANY("+next(f.Region)+")")
	}
	// Provider and category are catalog properties, not columns; expand them into
	// the set of resource type codes they cover.
	if codes := codesFor(f.Provider, f.Category); codes != nil {
		conds = append(conds, "r.resource_type = ANY("+next(codes)+")")
	}
	if f.GroupID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM resource_group_members m "+
			"WHERE m.resource_id = r.id AND m.group_id = "+next(f.GroupID)+"::uuid)")
	}
	if f.Tag != "" {
		if k, v, ok := strings.Cut(f.Tag, "="); ok {
			conds = append(conds, "EXISTS (SELECT 1 FROM resource_tags rt "+
				"JOIN tags tg ON tg.id = rt.tag_id WHERE rt.resource_id = r.id "+
				"AND tg.key = "+next(k)+" AND tg.value = "+next(v)+")")
		}
	}
	if f.OnlyIssues {
		conds = append(conds, "r.status IN ('down','critical','trouble')")
	}
	return strings.Join(conds, " AND "), args
}

// codesFor turns provider and category filters into resource type codes.
// Returns nil when neither filter is set.
func codesFor(providers, categories []string) []string {
	if len(providers) == 0 && len(categories) == 0 {
		return nil
	}
	inP := func(p string) bool {
		if len(providers) == 0 {
			return true
		}
		for _, x := range providers {
			if x == p {
				return true
			}
		}
		return false
	}
	inC := func(c string) bool {
		if len(categories) == 0 {
			return true
		}
		for _, x := range categories {
			if x == c {
				return true
			}
		}
		return false
	}
	var out []string
	for _, t := range catalog.All() {
		if inP(t.Provider) && inC(t.Category) {
			out = append(out, t.Code)
		}
	}
	if out == nil {
		// No type matches: return an impossible code rather than dropping the
		// filter, which would silently widen the result set.
		out = []string{"__none__"}
	}
	return out
}

func orderBy(sort string) string {
	switch sort {
	case "name":
		return "r.display_name ASC"
	case "availability":
		return "avail ASC NULLS LAST, r.display_name ASC"
	case "polled":
		return "r.last_polled_at DESC NULLS LAST"
	default:
		// Worst first, which is the order an operator wants by default.
		return `CASE r.status
			WHEN 'down' THEN 0 WHEN 'critical' THEN 1 WHEN 'trouble' THEN 2
			WHEN 'unknown' THEN 3 WHEN 'discovery' THEN 4 WHEN 'maintenance' THEN 5
			WHEN 'suspended' THEN 6 ELSE 7 END, r.display_name ASC`
	}
}

// Resources returns one page plus counts over the whole filtered set.
func (s *Store) Resources(f store.ResourceFilter) model.ResourceList {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out := model.ResourceList{
		Items:  []model.Resource{},
		Counts: model.ResourceCounts{ByStatus: map[string]int{}},
	}
	where, args := resourceWhere(f)

	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page := max(f.Page, 1)

	s.read(ctx, "resources", func(tx pgx.Tx) error {
		// Counts over the filtered set, not the page.
		rows, err := tx.Query(ctx,
			`SELECT r.status, count(*), coalesce(sum(a.open_alarms),0),
			        avg(NULLIF(d.availability_pct,0))
			 FROM resources r
			 LEFT JOIN (SELECT resource_id, count(*) AS open_alarms FROM alerts
			            WHERE state IN ('open','acknowledged') GROUP BY resource_id) a
			        ON a.resource_id = r.id
			 LEFT JOIN (SELECT resource_id, avg(availability_pct) AS availability_pct
			            FROM availability_daily WHERE day >= current_date - 1
			            GROUP BY resource_id) d ON d.resource_id = r.id
			 WHERE `+where+`
			 GROUP BY r.status`, args...)
		if err != nil {
			return err
		}
		var availSum float64
		var availN int
		for rows.Next() {
			var status string
			var n, alarms int
			var avail *float64
			if err := rows.Scan(&status, &n, &alarms, &avail); err != nil {
				rows.Close()
				return err
			}
			out.Counts.ByStatus[status] = n
			out.Counts.Total += n
			out.Counts.OpenAlarms += alarms
			switch status {
			case model.StatusMaintenance:
				out.Counts.Maintenance += n
			case model.StatusDiscovery:
				out.Counts.Discovery += n
			case model.StatusSuspended:
				out.Counts.Suspended += n
			case model.StatusUnknown:
				out.Counts.ConfigErrors += n
			}
			if avail != nil {
				availSum += *avail * float64(n)
				availN += n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if availN > 0 {
			out.Counts.Availability = round3(availSum / float64(availN))
		}

		// The page itself.
		lim := len(args) + 1
		pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
		rows, err = tx.Query(ctx,
			`SELECT `+resourceColumns+`,
			        coalesce(d.availability_pct,0) AS avail,
			        coalesce(a.open_alarms,0)
			 FROM resources r
			 LEFT JOIN (SELECT resource_id, count(*) AS open_alarms FROM alerts
			            WHERE state IN ('open','acknowledged') GROUP BY resource_id) a
			        ON a.resource_id = r.id
			 LEFT JOIN (SELECT resource_id, avg(availability_pct) AS availability_pct
			            FROM availability_daily WHERE day >= current_date - 1
			            GROUP BY resource_id) d ON d.resource_id = r.id
			 WHERE `+where+`
			 ORDER BY `+orderBy(f.Sort)+`
			 LIMIT $`+fmt.Sprint(lim)+` OFFSET $`+fmt.Sprint(lim+1), pageArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r model.Resource
			var accountID *string
			var avail float64
			var alarms int
			if err := rows.Scan(
				&r.ID, &accountID, &r.ResourceType, &r.NativeID, &r.DisplayName,
				&r.Region, &r.AvailabilityZone, &r.Status, &r.StatusSince,
				&r.LastPolledAt, &r.Suspended, &r.Attributes, &r.DiscoveredAt, &r.LastSeenAt,
				&avail, &alarms); err != nil {
				return err
			}
			if accountID != nil {
				r.CloudAccountID = *accountID
			}
			if t, ok := catalog.Get(r.ResourceType); ok {
				r.Provider, r.TypeName, r.Category = t.Provider, t.DisplayName, t.Category
			}
			if r.Attributes == nil {
				r.Attributes = map[string]any{}
			}
			r.Tags = map[string]string{}
			r.Availability24h = round3(avail)
			r.OpenAlarms = alarms
			out.Items = append(out.Items, r)
		}
		return rows.Err()
	})

	// Tags for just this page, rather than joining them into the main query and
	// multiplying rows.
	s.attachTags(ctx, out.Items)

	out.Total = out.Counts.Total
	out.Page = page
	out.PageSize = pageSize
	return out
}

func (s *Store) attachTags(ctx context.Context, items []model.Resource) {
	if len(items) == 0 {
		return
	}
	ids := make([]string, 0, len(items))
	idx := make(map[string]int, len(items))
	for i, r := range items {
		ids = append(ids, r.ID)
		idx[r.ID] = i
	}
	s.read(ctx, "resource tags", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT rt.resource_id::text, tg.key, tg.value
			 FROM resource_tags rt JOIN tags tg ON tg.id = rt.tag_id
			 WHERE rt.resource_id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, k, v string
			if err := rows.Scan(&id, &k, &v); err != nil {
				return err
			}
			if i, ok := idx[id]; ok {
				items[i].Tags[k] = v
			}
		}
		return rows.Err()
	})
}

// Resource returns one resource by id.
func (s *Store) Resource(id string) (model.Resource, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var out model.Resource
	found := false
	s.read(ctx, "resource", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+resourceColumns+`
			 FROM resources r WHERE r.id = $1::uuid AND r.deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			r, err := scanResource(rows)
			if err != nil {
				return err
			}
			out, found = r, true
		}
		return rows.Err()
	})
	if !found {
		return out, false
	}

	one := []model.Resource{out}
	s.attachTags(ctx, one)
	out = one[0]

	s.read(ctx, "resource availability", func(tx pgx.Tx) error {
		var avail *float64
		var alarms int
		if err := tx.QueryRow(ctx,
			`SELECT (SELECT avg(availability_pct) FROM availability_daily
			         WHERE resource_id = $1::uuid AND day >= current_date - 1),
			        (SELECT count(*) FROM alerts
			         WHERE resource_id = $1::uuid AND state IN ('open','acknowledged'))`,
			id).Scan(&avail, &alarms); err != nil {
			return err
		}
		if avail != nil {
			out.Availability24h = round3(*avail)
		}
		out.OpenAlarms = alarms
		return nil
	})
	return out, true
}

// Summary computes the dashboard payload.
func (s *Store) Summary() model.StatusSummary {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sum := model.StatusSummary{
		ByStatus:    map[string]int{},
		ByProvider:  map[string]map[string]int{},
		ByCategory:  map[string]map[string]int{},
		OpenAlarms:  map[string]int{},
		GeneratedAt: time.Now().UTC(),
	}

	s.read(ctx, "summary", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT resource_type, status, count(*)
			 FROM resources WHERE deleted_at IS NULL
			 GROUP BY resource_type, status`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var code, status string
			var n int
			if err := rows.Scan(&code, &status, &n); err != nil {
				rows.Close()
				return err
			}
			sum.Total += n
			sum.ByStatus[status] += n
			t, ok := catalog.Get(code)
			if !ok {
				continue
			}
			if sum.ByProvider[t.Provider] == nil {
				sum.ByProvider[t.Provider] = map[string]int{}
			}
			sum.ByProvider[t.Provider][status] += n
			if sum.ByCategory[t.Category] == nil {
				sum.ByCategory[t.Category] = map[string]int{}
			}
			sum.ByCategory[t.Category][status] += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx,
			`SELECT severity, count(*), count(*) FILTER (WHERE state = 'open')
			 FROM alerts WHERE state IN ('open','acknowledged') GROUP BY severity`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sev string
			var n, unacked int
			if err := rows.Scan(&sev, &n, &unacked); err != nil {
				rows.Close()
				return err
			}
			sum.OpenAlarms[sev] = n
			sum.Unacked += unacked
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		var avail *float64
		if err := tx.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM outages WHERE ended_at IS NULL),
			        (SELECT avg(availability_pct) FROM availability_daily
			         WHERE day >= current_date - 1)`).
			Scan(&sum.OngoingOutages, &avail); err != nil {
			return err
		}
		if avail != nil {
			sum.Availability = round3(*avail)
		}
		return nil
	})

	return sum
}

/* ---------------------------------------------------------------- alarms */

// Alarms returns one page of alarms, newest first.
func (s *Store) Alarms(f store.AlarmFilter) model.Page[model.Alarm] {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out := model.Page[model.Alarm]{Items: []model.Alarm{}}
	var conds []string
	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(f.State) > 0 {
		conds = append(conds, "a.state = ANY("+next(f.State)+")")
	}
	if len(f.Severity) > 0 {
		conds = append(conds, "a.severity = ANY("+next(f.Severity)+")")
	}
	if f.ResourceID != "" {
		conds = append(conds, "a.resource_id = "+next(f.ResourceID)+"::uuid")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := next("%" + strings.ToLower(q) + "%")
		conds = append(conds, fmt.Sprintf("(lower(r.display_name) LIKE %s OR lower(a.message) LIKE %s)", p, p))
	}
	if codes := codesFor(f.Provider, nil); codes != nil {
		conds = append(conds, "r.resource_type = ANY("+next(codes)+")")
	}
	where := "TRUE"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}

	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page := max(f.Page, 1)

	s.read(ctx, "alarms", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM alerts a JOIN resources r ON r.id = a.resource_id
			 WHERE `+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		lim := len(args) + 1
		pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
		rows, err := tx.Query(ctx,
			`SELECT a.id::text, a.resource_id::text, r.display_name, r.resource_type,
			        coalesce(r.region,''), a.dedup_key, a.severity, a.state,
			        coalesce(a.metric_key,''), a.observed_value, a.threshold_value,
			        a.message, a.poll_count, a.opened_at, a.acknowledged_at,
			        a.resolved_at, a.escalation_level,
			        (SELECT u.display_name FROM users u WHERE u.id = a.acknowledged_by),
			        -- Mute, maintenance suppression, notes and delivery count come
			        -- back with the list so a row can show whether anyone was told
			        -- without a request per alarm.
			        a.muted_until, a.suppressed_by_maintenance::text, a.rca,
			        a.last_notified_at,
			        (SELECT count(*) FROM alert_notifications n
			          WHERE n.alert_id = a.id AND n.state = 'sent')::int
			 FROM alerts a JOIN resources r ON r.id = a.resource_id
			 WHERE `+where+`
			 ORDER BY CASE a.severity WHEN 'down' THEN 0 WHEN 'critical' THEN 1
			                          WHEN 'trouble' THEN 2 ELSE 3 END,
			          a.opened_at DESC
			 LIMIT $`+fmt.Sprint(lim)+` OFFSET $`+fmt.Sprint(lim+1), pageArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a model.Alarm
			var ackBy, mw *string
			var rcaRaw []byte
			if err := rows.Scan(&a.ID, &a.ResourceID, &a.ResourceName, &a.ResourceType,
				&a.Region, &a.DedupKey, &a.Severity, &a.State, &a.MetricKey,
				&a.ObservedValue, &a.ThresholdValue, &a.Message, &a.PollCount,
				&a.OpenedAt, &a.AckedAt, &a.ResolvedAt, &a.EscalationLvl, &ackBy,
				&a.MutedUntil, &mw, &rcaRaw, &a.LastNotifiedAt, &a.Notified); err != nil {
				return err
			}
			if ackBy != nil {
				a.AckedBy = *ackBy
			}
			if mw != nil {
				a.SuppressedBy = *mw
			}
			a.RCA, a.MuteReason = parseRCA(rcaRaw)
			if t, ok := catalog.Get(a.ResourceType); ok {
				a.Provider = t.Provider
				if m, found := t.Metric(a.MetricKey); found {
					a.MetricLabel, a.Unit = m.Label, m.Unit
				}
			}
			out.Items = append(out.Items, a)
		}
		return rows.Err()
	})

	out.Page, out.PageSize = page, pageSize
	return out
}

// Acknowledge marks an open alarm as acknowledged.
func (s *Store) Acknowledge(id, user string) (model.Alarm, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var state string
	exists := false
	updated := false
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT state FROM alerts WHERE id = $1::uuid`, id).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		exists = true
		if state != model.AlarmOpen {
			return nil
		}
		// The acknowledging user is resolved from the users table, not trusted
		// from the request body; an audit trail built on client-supplied names is
		// worthless.
		tag, err := tx.Exec(ctx,
			`UPDATE alerts SET state = 'acknowledged', acknowledged_at = now(),
			        acknowledged_by = (SELECT id FROM users WHERE display_name = $2 OR email = $2 LIMIT 1),
			        updated_at = now()
			 WHERE id = $1::uuid AND state = 'open'`, id, user)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		s.log.Error("acknowledge failed", "alarm", id, "err", err)
		return model.Alarm{}, false
	}
	if !exists {
		return model.Alarm{}, false
	}

	// Re-read the alarm so the caller sees the stored state rather than an
	// optimistic local copy.
	var out model.Alarm
	s.read(ctx, "alarm after ack", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT a.id::text, a.resource_id::text, r.display_name, r.resource_type,
			        coalesce(r.region,''), a.dedup_key, a.severity, a.state,
			        coalesce(a.metric_key,''), a.observed_value, a.threshold_value,
			        a.message, a.poll_count, a.opened_at, a.acknowledged_at,
			        a.resolved_at, a.escalation_level,
			        (SELECT u.display_name FROM users u WHERE u.id = a.acknowledged_by),
			        -- Mute, maintenance suppression, notes and delivery count come
			        -- back with the list so a row can show whether anyone was told
			        -- without a request per alarm.
			        a.muted_until, a.suppressed_by_maintenance::text, a.rca,
			        a.last_notified_at,
			        (SELECT count(*) FROM alert_notifications n
			          WHERE n.alert_id = a.id AND n.state = 'sent')::int
			 FROM alerts a JOIN resources r ON r.id = a.resource_id WHERE a.id = $1::uuid`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			var ackBy *string
			if err := rows.Scan(&out.ID, &out.ResourceID, &out.ResourceName, &out.ResourceType,
				&out.Region, &out.DedupKey, &out.Severity, &out.State, &out.MetricKey,
				&out.ObservedValue, &out.ThresholdValue, &out.Message, &out.PollCount,
				&out.OpenedAt, &out.AckedAt, &out.ResolvedAt, &out.EscalationLvl, &ackBy); err != nil {
				return err
			}
			if ackBy != nil {
				out.AckedBy = *ackBy
			}
		}
		return rows.Err()
	})
	return out, updated
}

// Outages returns outage history, newest first.
func (s *Store) Outages(resourceID string, ongoing bool, page, pageSize int) model.Page[model.Outage] {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out := model.Page[model.Outage]{Items: []model.Outage{}}
	var conds []string
	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if resourceID != "" {
		conds = append(conds, "o.resource_id = "+next(resourceID)+"::uuid")
	}
	if ongoing {
		conds = append(conds, "o.ended_at IS NULL")
	}
	where := "TRUE"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page = max(page, 1)

	s.read(ctx, "outages", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM outages o WHERE `+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		lim := len(args) + 1
		pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
		rows, err := tx.Query(ctx,
			`SELECT o.id::text, o.resource_id::text, r.display_name, r.resource_type,
			        o.started_at, o.ended_at,
			        coalesce(o.duration_sec, extract(epoch FROM (now() - o.started_at))::int),
			        o.severity, o.classified_as, coalesce(o.root_cause,''), coalesce(o.comment,'')
			 FROM outages o JOIN resources r ON r.id = o.resource_id
			 WHERE `+where+`
			 ORDER BY o.started_at DESC
			 LIMIT $`+fmt.Sprint(lim)+` OFFSET $`+fmt.Sprint(lim+1), pageArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o model.Outage
			var code string
			if err := rows.Scan(&o.ID, &o.ResourceID, &o.ResourceName, &code,
				&o.StartedAt, &o.EndedAt, &o.DurationSec, &o.Severity,
				&o.ClassifiedAs, &o.RootCause, &o.Comment); err != nil {
				return err
			}
			if t, ok := catalog.Get(code); ok {
				o.Provider = t.Provider
			}
			out.Items = append(out.Items, o)
		}
		return rows.Err()
	})

	out.Page, out.PageSize = page, pageSize
	return out
}

/* -------------------------------------------------------------- metrics */

// Metrics reads a stored series. Returns an empty series rather than generated
// data: inventing numbers for real infrastructure is worse than showing none.
func (s *Store) Metrics(resourceID, metricKey string, from, to time.Time, points int) (model.MetricSeries, error) {
	res, ok := s.Resource(resourceID)
	if !ok {
		return model.MetricSeries{}, fmt.Errorf("resource %q not found", resourceID)
	}
	t, ok := catalog.Get(res.ResourceType)
	if !ok {
		return model.MetricSeries{}, fmt.Errorf("unknown resource type %q", res.ResourceType)
	}
	m, found := t.Metric(metricKey)
	if !found {
		return model.MetricSeries{}, fmt.Errorf("metric %q not defined on %s", metricKey, res.ResourceType)
	}

	series := model.MetricSeries{
		ResourceID: resourceID, MetricKey: m.Key, Label: m.Label, Unit: m.Unit,
		Trouble: m.Trouble, Critical: m.Critical, Samples: []model.Sample{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.read(ctx, "metrics", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT t, v FROM metric_samples
			 WHERE native_id = $1 AND metric_key = $2 AND t BETWEEN $3 AND $4
			 ORDER BY t ASC`, res.NativeID, metricKey, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sp model.Sample
			if err := rows.Scan(&sp.T, &sp.V); err != nil {
				return err
			}
			series.Samples = append(series.Samples, sp)
		}
		return rows.Err()
	})
	return series, nil
}

// PutSamples stores collected metric samples.
func (s *Store) PutSamples(samples []collect.Sample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stored := 0
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, sp := range samples {
			// A re-read of an overlapping window is expected, so a repeated
			// timestamp updates rather than erroring.
			batch.Queue(
				`INSERT INTO metric_samples (tenant_id, native_id, metric_key, t, v)
				 VALUES (current_tenant_id(), $1, $2, $3, $4)
				 ON CONFLICT (tenant_id, native_id, metric_key, t) DO UPDATE SET v = EXCLUDED.v`,
				sp.NativeID, sp.MetricKey, sp.T, sp.V)
		}
		res := tx.SendBatch(ctx, batch)
		defer func() { _ = res.Close() }()
		for range samples {
			if _, err := res.Exec(); err != nil {
				return err
			}
			stored++
		}
		return nil
	})
	return stored, err
}

/* ------------------------------------------------------------- ingestion */

// UpsertResources merges a collection run's resources.
//
// Identity is (cloud_account_id, native_id): display names change, OCIDs do not.
// Resources absent from a successful run are soft-deleted so their availability
// history and cost attribution survive.
func (s *Store) UpsertResources(accountID string, incoming []model.Resource) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var created, updated int
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM cloud_accounts WHERE id = $1::uuid)`, accountID).
			Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("unknown cloud account %q", accountID)
		}

		seen := make([]string, 0, len(incoming))
		for _, in := range incoming {
			if in.NativeID == "" {
				continue
			}
			if _, ok := catalog.Get(in.ResourceType); !ok {
				continue
			}
			seen = append(seen, in.NativeID)

			attrs := in.Attributes
			if attrs == nil {
				attrs = map[string]any{}
			}
			var wasCreated bool
			// Two rules are encoded in the ON CONFLICT clause below, and both exist
			// because the obvious version produces visibly wrong output.
			//
			// status_since only moves when the status actually changes, otherwise
			// "down for 3 days" resets to zero on every poll.
			//
			// An incoming "unknown" does not overwrite a recent "up". Cloud metric
			// reads are unreliable per-run, so a pass that returned nothing is not
			// evidence the resource stopped working; without this the status flaps
			// every interval. After ConfirmationGrace the silence is treated as
			// real.
			if err := tx.QueryRow(ctx,
				`INSERT INTO resources
				   (tenant_id, cloud_account_id, resource_type, native_id, display_name,
				    region, availability_zone, status, status_since, attributes,
				    suspended, confirmed_at, discovered_at, last_seen_at, last_polled_at)
				 VALUES (current_tenant_id(), $1::uuid, $2, $3, $4, $5, $6, $7, now(), $8,
				         $9, CASE WHEN $7 = 'up' THEN now() END, now(), now(), now())
				 ON CONFLICT (tenant_id, cloud_account_id, native_id) DO UPDATE SET
				   display_name = EXCLUDED.display_name,
				   region       = EXCLUDED.region,
				   availability_zone = EXCLUDED.availability_zone,
				   attributes   = EXCLUDED.attributes,
				   resource_type = EXCLUDED.resource_type,
				   status = CASE
				     WHEN EXCLUDED.status = 'unknown'
				          AND resources.status = 'up'
				          AND resources.confirmed_at > now() - $10::interval
				     THEN resources.status
				     ELSE EXCLUDED.status END,
				   -- Refreshed only by a real confirmation, so the grace window
				   -- measures silence rather than time since the last poll.
				   confirmed_at = CASE
				     WHEN EXCLUDED.status = 'up' THEN now()
				     ELSE resources.confirmed_at END,
				   -- The suspended flag was previously absent from this statement, so
				   -- a resource the collector reported as intentionally stopped got
				   -- status 'suspended' while the boolean stayed false. The alert
				   -- evaluator reads the boolean, so those resources kept alerting.
				   suspended    = EXCLUDED.suspended,
				   status_since = CASE
				     WHEN resources.status <> (CASE
				            WHEN EXCLUDED.status = 'unknown'
				                 AND resources.status = 'up'
				                 AND resources.confirmed_at > now() - $10::interval
				            THEN resources.status
				            ELSE EXCLUDED.status END)
				     THEN now() ELSE resources.status_since END,
				   last_seen_at = now(),
				   last_polled_at = now(),
				   deleted_at   = NULL,
				   updated_at   = now()
				 RETURNING (xmax = 0)`,
				accountID, in.ResourceType, in.NativeID, in.DisplayName,
				nullIfEmpty(in.Region), nullIfEmpty(in.AvailabilityZone),
				orDefault(in.Status, model.StatusUnknown), attrs, in.Suspended,
				store.ConfirmationGrace.String()).Scan(&wasCreated); err != nil {
				return fmt.Errorf("upsert %s: %w", in.NativeID, err)
			}
			if wasCreated {
				created++
			} else {
				updated++
			}

			if err := s.syncTags(ctx, tx, accountID, in.NativeID, in.Tags); err != nil {
				return err
			}
		}

		// Soft-delete what this run did not see.
		if _, err := tx.Exec(ctx,
			`UPDATE resources SET deleted_at = now(), updated_at = now()
			 WHERE cloud_account_id = $1::uuid AND deleted_at IS NULL
			   AND NOT (native_id = ANY($2))`, accountID, seen); err != nil {
			return err
		}
		return nil
	})
	return created, updated, err
}

// syncTags reconciles a resource's cloud-imported tags.
func (s *Store) syncTags(ctx context.Context, tx pgx.Tx, accountID, nativeID string, tags map[string]string) error {
	var resourceID string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM resources
		 WHERE cloud_account_id = $1::uuid AND native_id = $2`, accountID, nativeID).
		Scan(&resourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM resource_tags rt USING tags tg
		 WHERE rt.tag_id = tg.id AND rt.resource_id = $1::uuid AND tg.source = 'cloud'`,
		resourceID); err != nil {
		return err
	}
	for k, v := range tags {
		var tagID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO tags (tenant_id, key, value, source)
			 VALUES (current_tenant_id(), $1, $2, 'cloud')
			 ON CONFLICT (tenant_id, key, value, source) DO UPDATE SET key = EXCLUDED.key
			 RETURNING id::text`, k, v).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO resource_tags (tag_id, resource_id) VALUES ($1::uuid, $2::uuid)
			 ON CONFLICT DO NOTHING`, tagID, resourceID); err != nil {
			return err
		}
	}
	return nil
}

// RecordRun updates an account's discovery health from a collection run.
func (s *Store) RecordRun(run collect.RunReport) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	lastError := run.Error
	if run.State == "partial" && lastError == "" && len(run.RegionsFailed) > 0 {
		// Never leave a degraded account without a reason on screen.
		lastError = fmt.Sprintf("could not reach %d of %d regions: %s",
			len(run.RegionsFailed), len(run.RegionsFailed)+len(run.RegionsOK),
			strings.Join(run.RegionsFailed, ", "))
	}

	// What discovery saw but does not monitor, kept as a snapshot so the console can
	// answer "what does this tenancy contain that we are not watching". Previously
	// this only reached the log, where nobody looks for an inventory question.
	unmapped, err := json.Marshal(run.UnmappedTypes)
	if err != nil {
		unmapped = []byte("{}")
	}

	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE cloud_accounts
			 SET last_discovery_at = $2, last_discovery_status = $3, last_error = $4,
			     consecutive_failures = CASE WHEN $3 = 'ok' THEN 0
			                                 ELSE consecutive_failures + 1 END,
			     discovered_total = $5, mapped_total = $6, ignored_total = $7,
			     unmapped_types = $8::jsonb,
			     updated_at = now()
			 WHERE id = $1::uuid`,
			run.AccountID, run.FinishedAt, run.State, nullIfEmpty(lastError),
			run.Discovered, run.Mapped, run.Ignored, unmapped)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("unknown cloud account %q", run.AccountID)
		}
		// Durable history, so "why did this stop updating" is answerable later.
		_, err = tx.Exec(ctx,
			`INSERT INTO collector_jobs
			   (tenant_id, cloud_account_id, job_type, scheduled_at, started_at,
			    finished_at, state, items_processed, error)
			 VALUES (current_tenant_id(), $1::uuid, 'discovery', $2, $2, $3,
			         CASE WHEN $4 = 'ok' THEN 'ok' ELSE 'failed' END, $5, $6)`,
			run.AccountID, run.StartedAt, run.FinishedAt, run.State, run.Mapped,
			nullIfEmpty(lastError))
		return err
	})
}

/* -------------------------------------------------------------- accounts */

const accountColumns = `
	a.id::text, a.provider, a.display_name, a.native_account_id, a.regions,
	a.enabled, a.last_discovery_at, coalesce(a.last_discovery_status,'never'),
	coalesce(a.last_error,''), a.credentials_ref, a.config,
	a.discovery_interval_sec, a.metric_interval_sec,
	(SELECT count(*) FROM resources r
	  WHERE r.cloud_account_id = a.id AND r.deleted_at IS NULL)`

func scanAccount(rows pgx.Rows) (model.CloudAccount, error) {
	var a model.CloudAccount
	err := rows.Scan(&a.ID, &a.Provider, &a.DisplayName, &a.NativeAccountID, &a.Regions,
		&a.Enabled, &a.LastDiscoveryAt, &a.DiscoveryState, &a.LastError,
		&a.CredentialsRef, &a.Config, &a.DiscoveryIntervalSec, &a.MetricIntervalSec,
		&a.ResourceCount)
	if a.Config == nil {
		a.Config = map[string]string{}
	}
	return a, err
}

// Accounts returns the connected cloud accounts.
func (s *Store) Accounts() []model.CloudAccount {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := []model.CloudAccount{}
	s.read(ctx, "accounts", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+accountColumns+` FROM cloud_accounts a ORDER BY a.provider, a.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out
}

// Account returns one account by id.
func (s *Store) Account(id string) (model.CloudAccount, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out model.CloudAccount
	found := false
	s.read(ctx, "account", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+accountColumns+` FROM cloud_accounts a WHERE a.id = $1::uuid`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				return err
			}
			out, found = a, true
		}
		return rows.Err()
	})
	return out, found
}

// AddAccount registers a cloud account and queues discovery.
func (s *Store) AddAccount(in store.AccountInput) (model.CloudAccount, error) {
	if err := store.Validate(in, s.Accounts(), ""); err != nil {
		return model.CloudAccount{}, err
	}
	if in.DiscoveryIntervalSec <= 0 {
		in.DiscoveryIntervalSec = 3600
	}
	if in.MetricIntervalSec <= 0 {
		in.MetricIntervalSec = 300
	}
	enabled := in.Enabled == nil || *in.Enabled

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var id string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO cloud_accounts
			   (tenant_id, provider, display_name, native_account_id, credentials_ref,
			    config, regions, enabled, discovery_interval_sec, metric_interval_sec,
			    last_discovery_status)
			 VALUES (current_tenant_id(), $1,$2,$3,$4,$5,$6,$7,$8,$9,'never')
			 RETURNING id::text`,
			in.Provider, strings.TrimSpace(in.DisplayName),
			strings.TrimSpace(in.NativeAccountID), strings.TrimSpace(in.CredentialsRef),
			in.Config, in.Regions, enabled,
			in.DiscoveryIntervalSec, in.MetricIntervalSec).Scan(&id)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return model.CloudAccount{}, &store.ValidationError{
				Fields: map[string]string{"native_account_id": "This account is already connected."}}
		}
		return model.CloudAccount{}, err
	}
	a, _ := s.Account(id)
	return a, nil
}

// UpdateAccount edits an account. A blank CredentialsRef keeps the stored path.
func (s *Store) UpdateAccount(id string, in store.AccountInput) (model.CloudAccount, error) {
	current, ok := s.Account(id)
	if !ok {
		return model.CloudAccount{}, store.ErrAccountNotFound
	}
	if in.CredentialsRef == "" {
		in.CredentialsRef = current.CredentialsRef
	}
	if err := store.Validate(in, s.Accounts(), id); err != nil {
		return model.CloudAccount{}, err
	}
	enabled := current.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	discInt := orInt(in.DiscoveryIntervalSec, current.DiscoveryIntervalSec)
	metInt := orInt(in.MetricIntervalSec, current.MetricIntervalSec)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE cloud_accounts SET display_name=$2, native_account_id=$3,
			        credentials_ref=$4, config=$5, regions=$6, enabled=$7,
			        discovery_interval_sec=$8, metric_interval_sec=$9, updated_at=now()
			 WHERE id = $1::uuid`,
			id, strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.NativeAccountID),
			strings.TrimSpace(in.CredentialsRef), in.Config, in.Regions, enabled,
			discInt, metInt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrAccountNotFound
		}
		return nil
	})
	if err != nil {
		return model.CloudAccount{}, err
	}
	a, _ := s.Account(id)
	return a, nil
}

// DeleteAccount removes an account, reporting how many resources it covered.
func (s *Store) DeleteAccount(id string) (int, error) {
	a, ok := s.Account(id)
	if !ok {
		return 0, store.ErrAccountNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM cloud_accounts WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrAccountNotFound
		}
		return nil
	})
	return a.ResourceCount, err
}

// VerifyAccount runs the checks that do not require a cloud API.
func (s *Store) VerifyAccount(id string, stat func(string) (bool, bool, error)) (store.VerifyResult, error) {
	a, ok := s.Account(id)
	if !ok {
		return store.VerifyResult{}, store.ErrAccountNotFound
	}
	return store.BuildVerifyResult(a, stat), nil
}

// ServiceView lists every resource type the provider supports with this
// account's count of each. Zero-count types are included: knowing a service is
// integrated and empty differs from not knowing about it at all.
func (s *Store) ServiceView(accountID string) ([]store.ServiceTile, error) {
	a, ok := s.Account(accountID)
	if !ok {
		return nil, store.ErrAccountNotFound
	}
	counts := map[string]int{}
	unhealthy := map[string]int{}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.read(ctx, "service view", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT resource_type, count(*),
			        count(*) FILTER (WHERE status IN ('down','critical','trouble'))
			 FROM resources
			 WHERE cloud_account_id = $1::uuid AND deleted_at IS NULL
			 GROUP BY resource_type`, accountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var n, bad int
			if err := rows.Scan(&code, &n, &bad); err != nil {
				return err
			}
			counts[code], unhealthy[code] = n, bad
		}
		return rows.Err()
	})

	out := []store.ServiceTile{}
	for _, t := range catalog.ByProvider(a.Provider) {
		out = append(out, store.ServiceTile{
			ResourceType: t.Code, DisplayName: t.DisplayName, Icon: t.Icon,
			Category: t.Category, Count: counts[t.Code],
			Unhealthy: unhealthy[t.Code], Enabled: true,
		})
	}
	return out, nil
}

/* ---------------------------------------------------------------- groups */

// Groups returns the resource groups with health rolled up.
func (s *Store) Groups() []model.ResourceGroup {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := []model.ResourceGroup{}
	s.read(ctx, "groups", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT g.id::text, g.display_name, coalesce(g.description,''),
			        coalesce(g.parent_group_id::text,''),
			        (SELECT count(*) FROM resource_group_members m
			          JOIN resources r ON r.id = m.resource_id
			          WHERE m.group_id = g.id AND r.deleted_at IS NULL),
			        (SELECT count(*) FROM resource_group_members m
			          JOIN resources r ON r.id = m.resource_id
			          WHERE m.group_id = g.id AND r.deleted_at IS NULL
			            AND r.status IN ('down','critical','trouble')),
			        coalesce((SELECT r.status FROM resource_group_members m
			          JOIN resources r ON r.id = m.resource_id
			          WHERE m.group_id = g.id AND r.deleted_at IS NULL
			          ORDER BY CASE r.status
			            WHEN 'down' THEN 0 WHEN 'critical' THEN 1 WHEN 'trouble' THEN 2
			            WHEN 'unknown' THEN 3 WHEN 'discovery' THEN 4
			            WHEN 'maintenance' THEN 5 WHEN 'suspended' THEN 6 ELSE 7 END
			          LIMIT 1), 'unknown')
			 FROM resource_groups g ORDER BY g.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g model.ResourceGroup
			if err := rows.Scan(&g.ID, &g.DisplayName, &g.Description, &g.ParentGroupID,
				&g.ResourceCount, &g.Unhealthy, &g.Status); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out
}

// Tags returns tag keys mapped to their distinct values, for filter dropdowns.
func (s *Store) Tags() map[string][]string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := map[string][]string{}
	s.read(ctx, "tags", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT key, value FROM tags WHERE value <> '' ORDER BY key, value`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			out[k] = append(out[k], v)
		}
		return rows.Err()
	})
	return out
}

// Regions returns the region list offered per provider.
func (s *Store) Regions() map[string][]string {
	out := map[string][]string{}
	for p, r := range store.KnownRegions {
		out[p] = append([]string(nil), r...)
	}
	return out
}

/* --------------------------------------------------------------- helpers */

func nullIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

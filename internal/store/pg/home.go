package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Home section queries.
//
// The tables here all predate this file. The alerter has suppressed alerts inside
// maintenance windows and the reports have read sla_definitions since they were
// written; what did not exist was any way to create either outside psql. A feature
// that only works from a database client is not a feature the product has.

var _ store.HomeStore = (*Store)(nil)

// typeMeta resolves a resource type code to its display name and provider.
//
// The catalog is the single authority on how a type is named, so every screen
// spells it the same way and a catalog rename does not leave stale text behind.
func typeMeta(code string) (typeName, provider string) {
	if t, ok := catalog.Get(code); ok {
		return t.DisplayName, t.Provider
	}
	return code, ""
}

// scopeIDs pulls a string array out of the jsonb scope shape shared by maintenance
// windows and SLA targets.
func scopeIDs(scope map[string]any, key string) []string {
	raw, ok := scope[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// expandScope resolves resource ids plus group memberships into one set, and
// returns the count with a few names.
//
// Counting matters because a window or target scoped to an empty group covers
// nothing, and nothing in the scope itself says so.
func (s *Store) expandScope(ctx context.Context, tx pgx.Tx, resourceIDs, groupIDs []string) (int, []string, error) {
	if len(resourceIDs) == 0 && len(groupIDs) == 0 {
		// An empty scope means "everything", matching how the alerter reads it.
		var n int
		var names []string
		rows, err := tx.Query(ctx, `
			SELECT display_name FROM resources WHERE deleted_at IS NULL
			ORDER BY display_name LIMIT 3`)
		if err != nil {
			return 0, nil, err
		}
		for rows.Next() {
			var nm string
			if err := rows.Scan(&nm); err != nil {
				rows.Close()
				return 0, nil, err
			}
			names = append(names, nm)
		}
		rows.Close()
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::int FROM resources WHERE deleted_at IS NULL`).Scan(&n); err != nil {
			return 0, nil, err
		}
		return n, names, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT count(*)::int, coalesce((array_agg(display_name ORDER BY display_name))[1:3], '{}')
		FROM resources r
		WHERE r.deleted_at IS NULL
		  AND (r.id = ANY($1::uuid[])
		       OR EXISTS (SELECT 1 FROM resource_group_members m
		                   WHERE m.resource_id = r.id AND m.group_id = ANY($2::uuid[])))`,
		resourceIDs, groupIDs)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var n int
	var names []string
	if rows.Next() {
		if err := rows.Scan(&n, &names); err != nil {
			return 0, nil, err
		}
	}
	return n, names, rows.Err()
}

/* -------------------------------------------------------------------------- */
/* Maintenance windows                                                         */
/* -------------------------------------------------------------------------- */

// windowState derives scheduled / active / finished from the clock.
//
// Derived rather than stored because a stored state needs a job to move it, and a
// job that stops leaves the UI confidently wrong about whether alerts are
// currently suppressed.
func windowState(start, end time.Time) string {
	now := time.Now()
	switch {
	case now.Before(start):
		return "scheduled"
	case now.After(end):
		return "finished"
	default:
		return "active"
	}
}

func (s *Store) MaintenanceWindows(ctx context.Context) ([]store.MaintenanceWindow, error) {
	out := []store.MaintenanceWindow{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT w.id::text, w.display_name, w.scope, w.starts_at, w.ends_at,
			       coalesce(w.recurrence,''), w.suppress_alerts, w.exclude_from_sla,
			       coalesce(u.display_name,''), w.created_at
			FROM maintenance_windows w
			LEFT JOIN users u ON u.id = w.created_by
			-- Active first, then upcoming, then history: the order someone asks
			-- "is anything suppressed right now" expects.
			ORDER BY (w.starts_at <= now() AND w.ends_at >= now()) DESC,
			         w.starts_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w store.MaintenanceWindow
			var scope map[string]any
			if err := rows.Scan(&w.ID, &w.DisplayName, &scope, &w.StartsAt, &w.EndsAt,
				&w.Recurrence, &w.SuppressAlerts, &w.ExcludeFromSLA,
				&w.CreatedBy, &w.CreatedAt); err != nil {
				return err
			}
			w.ResourceIDs = scopeIDs(scope, "resource_ids")
			w.GroupIDs = scopeIDs(scope, "group_ids")
			w.State = windowState(w.StartsAt, w.EndsAt)
			out = append(out, w)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			n, names, err := s.expandScope(ctx, tx, out[i].ResourceIDs, out[i].GroupIDs)
			if err != nil {
				return err
			}
			out[i].ResourceCount, out[i].SampleNames = n, names
		}
		return nil
	})
	return out, err
}

func validateMaintenance(in store.MaintenanceInput) (store.MaintenanceInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" {
		return in, invalid("give the window a name, so it is obvious later why alerts were quiet")
	}
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() {
		return in, invalid("a maintenance window needs a start and an end")
	}
	if !in.EndsAt.After(in.StartsAt) {
		return in, invalid("the window ends before it starts")
	}
	// A very long window is usually a typo in the year field, and it would silence
	// alerting for weeks without anyone noticing.
	if in.EndsAt.Sub(in.StartsAt) > 30*24*time.Hour {
		return in, invalid("a window longer than 30 days is almost certainly a mistake; " +
			"if alerting really should be off that long, suspend the monitors instead")
	}
	if in.EndsAt.Before(time.Now()) {
		return in, invalid("that window has already finished, so it would suppress nothing")
	}
	if in.Recurrence != "" && !strings.HasPrefix(strings.ToUpper(in.Recurrence), "FREQ=") {
		return in, invalid("recurrence must be an RRULE, for example FREQ=WEEKLY;BYDAY=SU")
	}
	return in, nil
}

func (s *Store) CreateMaintenance(ctx context.Context, userID string, in store.MaintenanceInput) (store.MaintenanceWindow, error) {
	var out store.MaintenanceWindow
	in, err := validateMaintenance(in)
	if err != nil {
		return out, err
	}
	scope := map[string]any{}
	if len(in.ResourceIDs) > 0 {
		scope["resource_ids"] = in.ResourceIDs
	}
	if len(in.GroupIDs) > 0 {
		scope["group_ids"] = in.GroupIDs
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return out, err
	}
	suppress, excl := true, true
	if in.SuppressAlerts != nil {
		suppress = *in.SuppressAlerts
	}
	if in.ExcludeFromSLA != nil {
		excl = *in.ExcludeFromSLA
	}

	var id string
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		// Refuse a window that covers nothing. It would sit in the list looking
		// like protection and suppress no alert at all.
		n, _, err := s.expandScope(ctx, tx, in.ResourceIDs, in.GroupIDs)
		if err != nil {
			return err
		}
		if n == 0 {
			return invalid("that scope covers no monitors, so the window would suppress nothing")
		}
		var uid *string
		if userID != "" {
			uid = &userID
		}
		return tx.QueryRow(ctx, `
			INSERT INTO maintenance_windows
			  (tenant_id, display_name, scope, starts_at, ends_at, recurrence,
			   suppress_alerts, exclude_from_sla, created_by)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, $2::jsonb, $3, $4,
			        nullif($5,''), $6, $7, $8::uuid)
			RETURNING id::text`,
			in.DisplayName, raw, in.StartsAt, in.EndsAt, in.Recurrence,
			suppress, excl, uid).Scan(&id)
	})
	if err != nil {
		return out, err
	}
	all, err := s.MaintenanceWindows(ctx)
	if err != nil {
		return out, err
	}
	for _, w := range all {
		if w.ID == id {
			return w, nil
		}
	}
	return out, ErrNotFound
}

func (s *Store) DeleteMaintenance(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM maintenance_windows WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

/* -------------------------------------------------------------------------- */
/* SLA targets                                                                 */
/* -------------------------------------------------------------------------- */

func (s *Store) SLATargets(ctx context.Context) ([]store.SLATarget, error) {
	out := []store.SLATarget{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, display_name, target_pct, period, scope, created_at
			FROM sla_definitions ORDER BY display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t store.SLATarget
			var scope map[string]any
			if err := rows.Scan(&t.ID, &t.DisplayName, &t.TargetPct, &t.Period,
				&scope, &t.CreatedAt); err != nil {
				return err
			}
			t.ResourceIDs = scopeIDs(scope, "resource_ids")
			t.GroupIDs = scopeIDs(scope, "group_ids")
			// The target restated as time per day. "Four minutes" is checkable by
			// eye in a way that "99.7 percent" is not.
			t.AllowedDownSecPerDay = int(86400 * (100 - t.TargetPct) / 100)
			out = append(out, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			n, _, err := s.expandScope(ctx, tx, out[i].ResourceIDs, out[i].GroupIDs)
			if err != nil {
				return err
			}
			out[i].ResourceCount = n
		}
		return nil
	})
	return out, err
}

var slaPeriods = map[string]bool{"daily": true, "weekly": true, "monthly": true, "quarterly": true}

func (s *Store) CreateSLATarget(ctx context.Context, in store.SLAInput) (store.SLATarget, error) {
	var out store.SLATarget
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" {
		return out, invalid("give the target a name")
	}
	if in.Period == "" {
		in.Period = "monthly"
	}
	if !slaPeriods[in.Period] {
		return out, invalid("period must be daily, weekly, monthly or quarterly")
	}
	if in.TargetPct <= 0 || in.TargetPct > 100 {
		return out, invalid("the target must be above 0 and at most 100")
	}
	// 100% is not a target, it is a promise no infrastructure keeps, and a target
	// that is breached by definition trains people to ignore the report.
	if in.TargetPct == 100 {
		return out, invalid("a 100%% target is breached by any single failed check; " +
			"99.9 allows about 86 seconds a day")
	}
	if in.TargetPct < 50 {
		return out, invalid("a target below 50%% is almost certainly a typo")
	}

	scope := map[string]any{}
	if len(in.ResourceIDs) > 0 {
		scope["resource_ids"] = in.ResourceIDs
	}
	if len(in.GroupIDs) > 0 {
		scope["group_ids"] = in.GroupIDs
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return out, err
	}

	var id string
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		n, _, err := s.expandScope(ctx, tx, in.ResourceIDs, in.GroupIDs)
		if err != nil {
			return err
		}
		if n == 0 {
			return invalid("that scope covers no monitors, so the target would measure nothing")
		}
		return tx.QueryRow(ctx, `
			INSERT INTO sla_definitions (tenant_id, display_name, target_pct, period, scope)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, $2, $3, $4::jsonb)
			RETURNING id::text`, in.DisplayName, in.TargetPct, in.Period, raw).Scan(&id)
	})
	if err != nil {
		if strings.Contains(err.Error(), "sla_definitions_tenant_id_display_name_key") {
			return out, invalid("a target named %q already exists", in.DisplayName)
		}
		return out, err
	}
	all, err := s.SLATargets(ctx)
	if err != nil {
		return out, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return out, ErrNotFound
}

func (s *Store) DeleteSLATarget(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM sla_definitions WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

/* -------------------------------------------------------------------------- */
/* Monitor groups                                                              */
/* -------------------------------------------------------------------------- */

// groupHealth applies the group's strategy to its member statuses.
func groupHealth(strategy string, threshold *int, g store.MonitorGroup) string {
	bad := g.Down + g.Critical
	switch strategy {
	case "count":
		n := 1
		if threshold != nil && *threshold > 0 {
			n = *threshold
		}
		if bad >= n {
			return "down"
		}
	case "percentage":
		pct := 50
		if threshold != nil && *threshold > 0 {
			pct = *threshold
		}
		if g.MemberCount > 0 && bad*100/g.MemberCount >= pct {
			return "down"
		}
	default: // worst_child
		if g.Down > 0 {
			return "down"
		}
		if g.Critical > 0 {
			return "critical"
		}
		if g.Trouble > 0 {
			return "trouble"
		}
	}
	if g.Trouble > 0 {
		return "trouble"
	}
	if g.Up > 0 {
		return "up"
	}
	// Every member unknown or suspended. Saying "up" here would be a claim nothing
	// supports.
	return "unknown"
}

const groupSelect = `
	SELECT g.id::text, g.display_name, coalesce(g.description,''),
	       g.health_strategy, g.health_threshold, g.created_at,
	       count(r.id)::int,
	       count(*) FILTER (WHERE r.status = 'up')::int,
	       count(*) FILTER (WHERE r.status = 'down')::int,
	       count(*) FILTER (WHERE r.status = 'trouble')::int,
	       count(*) FILTER (WHERE r.status = 'critical')::int,
	       count(*) FILTER (WHERE r.status = 'unknown')::int,
	       count(*) FILTER (WHERE r.status = 'suspended')::int
	FROM resource_groups g
	LEFT JOIN resource_group_members m ON m.group_id = g.id
	LEFT JOIN resources r ON r.id = m.resource_id AND r.deleted_at IS NULL`

func scanGroup(rows pgx.Rows) (store.MonitorGroup, error) {
	var g store.MonitorGroup
	if err := rows.Scan(&g.ID, &g.DisplayName, &g.Description, &g.HealthStrategy,
		&g.HealthThreshold, &g.CreatedAt, &g.MemberCount, &g.Up, &g.Down,
		&g.Trouble, &g.Critical, &g.Unknown, &g.Suspended); err != nil {
		return g, err
	}
	g.Health = groupHealth(g.HealthStrategy, g.HealthThreshold, g)
	return g, nil
}

func (s *Store) MonitorGroups(ctx context.Context) ([]store.MonitorGroup, error) {
	out := []store.MonitorGroup{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, groupSelect+`
			GROUP BY g.id, g.display_name, g.description, g.health_strategy,
			         g.health_threshold, g.created_at
			ORDER BY g.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) groupByID(ctx context.Context, id string) (store.MonitorGroup, error) {
	var g store.MonitorGroup
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, groupSelect+`
			WHERE g.id = $1::uuid
			GROUP BY g.id, g.display_name, g.description, g.health_strategy,
			         g.health_threshold, g.created_at`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return ErrNotFound
		}
		g, err = scanGroup(rows)
		return err
	})
	return g, err
}

var healthStrategies = map[string]bool{"worst_child": true, "percentage": true, "count": true}

func validateGroup(in store.GroupInput) (store.GroupInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" {
		return in, invalid("give the group a name")
	}
	if in.HealthStrategy == "" {
		in.HealthStrategy = "worst_child"
	}
	if !healthStrategies[in.HealthStrategy] {
		return in, invalid("health strategy must be worst_child, percentage or count")
	}
	if in.HealthStrategy == "percentage" {
		if in.HealthThreshold == nil || *in.HealthThreshold < 1 || *in.HealthThreshold > 100 {
			return in, invalid("a percentage strategy needs a threshold between 1 and 100")
		}
	}
	if in.HealthStrategy == "count" {
		if in.HealthThreshold == nil || *in.HealthThreshold < 1 {
			return in, invalid("a count strategy needs a threshold of at least 1")
		}
	}
	return in, nil
}

func (s *Store) CreateMonitorGroup(ctx context.Context, in store.GroupInput) (store.MonitorGroup, error) {
	var out store.MonitorGroup
	in, err := validateGroup(in)
	if err != nil {
		return out, err
	}
	var id string
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO resource_groups
			  (tenant_id, display_name, description, health_strategy, health_threshold)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, nullif($2,''), $3, $4)
			RETURNING id::text`,
			in.DisplayName, in.Description, in.HealthStrategy, in.HealthThreshold).Scan(&id); err != nil {
			return err
		}
		return setGroupMembers(ctx, tx, id, in.ResourceIDs)
	})
	if err != nil {
		if strings.Contains(err.Error(), "resource_groups_tenant_id_display_name_key") {
			return out, invalid("a group named %q already exists", in.DisplayName)
		}
		return out, err
	}
	return s.groupByID(ctx, id)
}

// setGroupMembers replaces a group's manual membership.
//
// Replace rather than merge: an edit screen shows the full set, so saving it must
// mean exactly that set, or removing a member silently fails.
func setGroupMembers(ctx context.Context, tx pgx.Tx, groupID string, ids []string) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM resource_group_members WHERE group_id = $1::uuid AND NOT dynamic`,
		groupID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO resource_group_members (group_id, resource_id, dynamic)
		SELECT $1::uuid, r.id, false FROM resources r
		 WHERE r.id = ANY($2::uuid[]) AND r.deleted_at IS NULL
		ON CONFLICT (group_id, resource_id) DO NOTHING`, groupID, ids)
	return err
}

func (s *Store) UpdateMonitorGroup(ctx context.Context, id string, in store.GroupInput) (store.MonitorGroup, error) {
	cur, err := s.groupByID(ctx, id)
	if err != nil {
		return cur, err
	}
	if in.DisplayName == "" {
		in.DisplayName = cur.DisplayName
	}
	if in.HealthStrategy == "" {
		in.HealthStrategy = cur.HealthStrategy
	}
	in, err = validateGroup(in)
	if err != nil {
		return cur, err
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE resource_groups
			   SET display_name = $2, description = nullif($3,''),
			       health_strategy = $4, health_threshold = $5, updated_at = now()
			 WHERE id = $1::uuid`,
			id, in.DisplayName, in.Description, in.HealthStrategy, in.HealthThreshold); err != nil {
			return err
		}
		if in.ResourceIDs != nil {
			return setGroupMembers(ctx, tx, id, in.ResourceIDs)
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	return s.groupByID(ctx, id)
}

func (s *Store) DeleteMonitorGroup(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		// Refuse while a maintenance window or SLA target points at it, otherwise
		// their scope silently becomes "everything" or "nothing".
		var used int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM maintenance_windows
			         WHERE scope->'group_ids' @> to_jsonb($1::text))
			     + (SELECT count(*) FROM sla_definitions
			         WHERE scope->'group_ids' @> to_jsonb($1::text))`, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return invalid("%d maintenance window(s) or SLA target(s) use this group; "+
				"change their scope first", used)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM resource_groups WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) GroupMembers(ctx context.Context, id string) ([]model.Resource, error) {
	out := []model.Resource{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.display_name, r.resource_type, r.status,
			       coalesce(r.region,''), r.suspended
			FROM resource_group_members m
			JOIN resources r ON r.id = m.resource_id
			WHERE m.group_id = $1::uuid AND r.deleted_at IS NULL
			ORDER BY r.display_name`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r model.Resource
			if err := rows.Scan(&r.ID, &r.DisplayName, &r.ResourceType, &r.Status,
				&r.Region, &r.Suspended); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

/* -------------------------------------------------------------------------- */
/* Alert delivery log                                                         */
/* -------------------------------------------------------------------------- */

func (s *Store) AlertLog(ctx context.Context, f store.AlertLogFilter) (store.AlertLogPage, error) {
	out := store.AlertLogPage{Entries: []store.AlertLogEntry{}}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	conds := []string{"true"}
	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.State != "" {
		conds = append(conds, "n.state = "+next(f.State))
	}
	if f.Before > 0 {
		conds = append(conds, "n.id < "+next(f.Before))
	}
	where := strings.Join(conds, " AND ")

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT n.id, n.state, n.alert_id::text, a.severity, coalesce(a.metric_key,''),
			       r.id::text, r.display_name,
			       coalesce(n.channel_id::text,''), coalesce(c.display_name,''),
			       coalesce(c.channel_type,''), coalesce(n.recipient,''),
			       n.level, n.attempts, coalesce(n.last_error,''), n.sent_at, n.created_at
			FROM alert_notifications n
			JOIN alerts a ON a.id = n.alert_id
			JOIN resources r ON r.id = a.resource_id
			LEFT JOIN notification_channels c ON c.id = n.channel_id
			WHERE `+where+`
			ORDER BY n.id DESC LIMIT `+fmt.Sprint(limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e store.AlertLogEntry
			if err := rows.Scan(&e.ID, &e.State, &e.AlertID, &e.Severity, &e.MetricKey,
				&e.ResourceID, &e.DisplayName, &e.ChannelID, &e.ChannelName,
				&e.ChannelType, &e.Recipient, &e.Level, &e.Attempts,
				&e.LastError, &e.SentAt, &e.CreatedAt); err != nil {
				return err
			}
			out.Entries = append(out.Entries, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Entries) > limit {
			out.Entries = out.Entries[:limit]
			out.NextBefore = out.Entries[limit-1].ID
		}

		// Counts cover the whole log, not this page, so the header does not change
		// as somebody pages through.
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE state = 'sent')::int,
			       count(*) FILTER (WHERE state = 'failed')::int,
			       count(*) FILTER (WHERE state = 'skipped')::int,
			       count(*) FILTER (WHERE state = 'pending')::int
			FROM alert_notifications`).
			Scan(&out.Sent, &out.Failed, &out.Skipped, &out.Pending); err != nil {
			return err
		}
		var enabled int
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::int FROM notification_channels WHERE enabled`).Scan(&enabled); err != nil {
			return err
		}
		out.DeliveryConfigured = enabled > 0
		return nil
	})
	return out, err
}

/* -------------------------------------------------------------------------- */
/* Outages                                                                     */
/* -------------------------------------------------------------------------- */

func (s *Store) OutageList(ctx context.Context, f store.OutageFilter) (store.OutagePage, error) {
	out := store.OutagePage{Rows: []store.OutageRow{}}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	conds := []string{"r.deleted_at IS NULL"}
	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Ongoing {
		conds = append(conds, "o.ended_at IS NULL")
	}
	if f.Since != nil {
		conds = append(conds, "coalesce(o.ended_at, now()) >= "+next(*f.Since))
	}
	where := strings.Join(conds, " AND ")

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int,
			       count(*) FILTER (WHERE o.ended_at IS NULL)::int,
			       -- Closed outages only: an ongoing one has no recovery time yet,
			       -- and averaging its elapsed time in understates MTTR.
			       avg(o.duration_sec) FILTER (WHERE o.ended_at IS NOT NULL),
			       coalesce(sum(coalesce(o.duration_sec,
			           extract(epoch FROM (now() - o.started_at))::int)),0)::int
			FROM outages o JOIN resources r ON r.id = o.resource_id
			WHERE `+where, args...).
			Scan(&out.Total, &out.Ongoing, &out.MeanMTTRSec, &out.TotalDownSec); err != nil {
			return err
		}

		args = append(args, limit, f.Offset)
		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.resource_id::text, r.display_name, r.resource_type,
			       coalesce(r.region,''), o.started_at, o.ended_at,
			       coalesce(o.duration_sec, extract(epoch FROM (now() - o.started_at))::int),
			       o.severity, o.classified_as, coalesce(o.root_cause,'')
			FROM outages o JOIN resources r ON r.id = o.resource_id
			WHERE `+where+`
			ORDER BY o.ended_at IS NULL DESC, o.started_at DESC
			LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
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
			row.TypeName, row.Provider = typeMeta(code)
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	return out, err
}

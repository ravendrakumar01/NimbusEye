package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/store"
)

var _ store.ConfigStore = (*Store)(nil)

/* -------------------------------------------------------------------------- */
/* Business hours                                                              */
/* -------------------------------------------------------------------------- */

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// inHours reports whether now falls inside any slot, in the schedule's own
// timezone.
//
// Evaluated here rather than in the browser because the browser's clock is the
// viewer's, and "are we inside business hours" is a question about the schedule's
// timezone. Someone checking from another country would otherwise see the wrong
// answer.
func inHours(tz string, slots []store.BusinessHoursSlot) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	// Go's Weekday is Sunday=0; the stored form is ISO-8601 with Monday=1.
	day := int(now.Weekday())
	if day == 0 {
		day = 7
	}
	cur := now.Format("15:04")
	for _, s := range slots {
		if s.Day != day {
			continue
		}
		if s.Start <= cur && cur < s.End {
			return true
		}
	}
	return false
}

func (s *Store) BusinessHours(ctx context.Context) ([]store.BusinessHours, error) {
	out := []store.BusinessHours{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id::text, b.display_name, b.timezone, b.time_config, b.created_at,
			       (SELECT count(*) FROM notification_profiles p
			         WHERE p.business_hours_id = b.id)::int
			FROM business_hours b ORDER BY b.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b store.BusinessHours
			var raw []byte
			if err := rows.Scan(&b.ID, &b.DisplayName, &b.Timezone, &raw,
				&b.CreatedAt, &b.UsedBy); err != nil {
				return err
			}
			b.Slots = []store.BusinessHoursSlot{}
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &b.Slots)
			}
			b.InHoursNow = inHours(b.Timezone, b.Slots)
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

func validateBusinessHours(in store.BusinessHoursInput) (store.BusinessHoursInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.DisplayName == "" {
		return in, invalid("give the schedule a name")
	}
	if in.Timezone == "" {
		in.Timezone = "Asia/Kolkata"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return in, invalid("%q is not a recognised timezone", in.Timezone)
	}
	if len(in.Slots) == 0 {
		return in, invalid("add at least one time window, otherwise the schedule " +
			"covers nothing and every profile using it would notify nobody")
	}
	for _, sl := range in.Slots {
		if sl.Day < 1 || sl.Day > 7 {
			return in, invalid("day must be 1 (Monday) to 7 (Sunday)")
		}
		if !hhmm.MatchString(sl.Start) || !hhmm.MatchString(sl.End) {
			return in, invalid("times must be in 24-hour HH:MM form")
		}
		// A slot ending before it starts silently covers nothing, which is the
		// same failure as an empty schedule but harder to spot.
		if sl.End <= sl.Start {
			return in, invalid("a window from %s to %s ends before it starts; "+
				"for an overnight shift add two windows, one per day", sl.Start, sl.End)
		}
	}
	return in, nil
}

func (s *Store) CreateBusinessHours(ctx context.Context, in store.BusinessHoursInput) (store.BusinessHours, error) {
	var out store.BusinessHours
	in, err := validateBusinessHours(in)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(in.Slots)
	if err != nil {
		return out, err
	}
	var id string
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO business_hours (tenant_id, display_name, timezone, time_config)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, $2, $3::jsonb)
			RETURNING id::text`, in.DisplayName, in.Timezone, raw).Scan(&id)
	})
	if err != nil {
		if strings.Contains(err.Error(), "business_hours_tenant_id_display_name_key") {
			return out, invalid("a schedule named %q already exists", in.DisplayName)
		}
		return out, err
	}
	all, err := s.BusinessHours(ctx)
	if err != nil {
		return out, err
	}
	for _, b := range all {
		if b.ID == id {
			return b, nil
		}
	}
	return out, ErrNotFound
}

func (s *Store) DeleteBusinessHours(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var used int
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::int FROM notification_profiles WHERE business_hours_id = $1::uuid`,
			id).Scan(&used); err != nil {
			return err
		}
		// Deleting it would set the reference to NULL, which means "always" — so a
		// profile deliberately limited to office hours would quietly start paging
		// at 3am.
		if used > 0 {
			return invalid("%d notification profile(s) use this schedule; "+
				"point them elsewhere first, or they would silently revert to notifying "+
				"around the clock", used)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM business_hours WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) SetProfileBusinessHours(ctx context.Context, profileID, businessHoursID string, notifyOutside bool) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var bh *string
		if businessHoursID != "" {
			bh = &businessHoursID
		}
		tag, err := tx.Exec(ctx, `
			UPDATE notification_profiles
			   SET business_hours_id = $2::uuid, notify_outside_business_hours = $3,
			       updated_at = now()
			 WHERE id = $1::uuid`, profileID, bh, notifyOutside)
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
/* Tags                                                                       */
/* -------------------------------------------------------------------------- */

func (s *Store) TagInventory(ctx context.Context) ([]store.TagKey, error) {
	byKey := map[string]*store.TagKey{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.key, t.value, t.source, coalesce(t.color,''),
			       (SELECT count(*) FROM resource_tags rt
			         JOIN resources r ON r.id = rt.resource_id AND r.deleted_at IS NULL
			         WHERE rt.tag_id = t.id)::int
			FROM tags t ORDER BY t.key, t.value`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tg store.Tag
			if err := rows.Scan(&tg.ID, &tg.Key, &tg.Value, &tg.Source,
				&tg.Color, &tg.Resources); err != nil {
				return err
			}
			k, ok := byKey[tg.Key]
			if !ok {
				k = &store.TagKey{Key: tg.Key, Source: tg.Source, Values: []store.Tag{}}
				byKey[tg.Key] = k
			}
			k.Values = append(k.Values, tg)
			k.Resources += tg.Resources
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]store.TagKey, 0, len(byKey))
	for _, k := range byKey {
		out = append(out, *k)
	}
	// Most-used first: with cloud-imported tags the long tail is noise, and the
	// keys worth grouping by are the ones on many resources.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resources != out[j].Resources {
			return out[i].Resources > out[j].Resources
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

/* -------------------------------------------------------------------------- */
/* Discovered resources                                                        */
/* -------------------------------------------------------------------------- */

func (s *Store) DiscoveryInventory(ctx context.Context) ([]store.DiscoveryInventory, error) {
	out := []store.DiscoveryInventory{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.display_name, a.provider, a.last_discovery_at,
			       coalesce(a.last_discovery_status,''), a.discovered_total,
			       a.mapped_total, a.ignored_total, a.unmapped_types
			FROM cloud_accounts a ORDER BY a.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d store.DiscoveryInventory
			var raw []byte
			if err := rows.Scan(&d.AccountID, &d.AccountName, &d.Provider, &d.LastRunAt,
				&d.Status, &d.Discovered, &d.Monitored, &d.Ignored, &raw); err != nil {
				return err
			}
			counts := map[string]int{}
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &counts)
			}
			d.Unmapped = make([]store.UnmappedType, 0, len(counts))
			for t, n := range counts {
				d.Unmapped = append(d.Unmapped, store.UnmappedType{ProviderType: t, Count: n})
				d.UnmappedTotal += n
			}
			sort.Slice(d.Unmapped, func(i, j int) bool {
				if d.Unmapped[i].Count != d.Unmapped[j].Count {
					return d.Unmapped[i].Count > d.Unmapped[j].Count
				}
				return d.Unmapped[i].ProviderType < d.Unmapped[j].ProviderType
			})
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

/* -------------------------------------------------------------------------- */
/* Health trend                                                                */
/* -------------------------------------------------------------------------- */

func (s *Store) HealthTrend(ctx context.Context, f store.ReportFilter) (store.HealthTrend, error) {
	out := store.HealthTrend{
		From: f.Range.From.Format("2006-01-02"),
		To:   f.Range.To.Format("2006-01-02"),
		Days: f.Range.Days(),
		Rows: []store.TrendSeries{},
	}

	args := []any{f.Range.From, f.Range.To}
	scope := s.reportScope(f, &args)
	args = append(args, limitOr(f.Limit, 40))

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(DISTINCT day)::int FROM availability_daily
			 WHERE day BETWEEN $1::date AND $2::date AND availability_pct IS NOT NULL`,
			f.Range.From, f.Range.To).Scan(&out.MeasuredDays); err != nil {
			return err
		}

		// Ordered by worst mean first: a trend list is read to find what is
		// degrading, not alphabetically.
		rows, err := tx.Query(ctx, `
			WITH scoped AS (
			  SELECT r.id, r.display_name, r.resource_type,
			         avg(d.availability_pct) AS mean
			  FROM resources r
			  JOIN availability_daily d ON d.resource_id = r.id
			       AND d.day BETWEEN $1::date AND $2::date
			  WHERE `+scope+` AND d.availability_pct IS NOT NULL
			  GROUP BY r.id, r.display_name, r.resource_type
			  ORDER BY mean ASC, r.display_name
			  LIMIT $`+fmt.Sprint(len(args))+`
			)
			SELECT s.id::text, s.display_name, s.resource_type,
			       d.day::text, d.availability_pct, d.down_sec
			FROM scoped s
			JOIN availability_daily d ON d.resource_id = s.id
			     AND d.day BETWEEN $1::date AND $2::date
			ORDER BY s.mean ASC, s.display_name, d.day ASC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		idx := map[string]int{}
		for rows.Next() {
			var id, name, code, day string
			var pct *float64
			var down int
			if err := rows.Scan(&id, &name, &code, &day, &pct, &down); err != nil {
				return err
			}
			i, ok := idx[id]
			if !ok {
				tn, pv := typeMeta(code)
				out.Rows = append(out.Rows, store.TrendSeries{
					ResourceID: id, DisplayName: name, TypeName: tn, Provider: pv,
					Points: []store.TrendPoint{},
				})
				i = len(out.Rows) - 1
				idx[id] = i
			}
			out.Rows[i].Points = append(out.Rows[i].Points,
				store.TrendPoint{Day: day, AvailabilityPct: round3p(pct), DownSec: down})
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}

	for i := range out.Rows {
		describeTrend(&out.Rows[i])
	}
	return out, nil
}

// describeTrend compares the second half of a series with the first.
//
// Only stated when there are at least four measured points. Calling two days a
// trend is the kind of claim that gets repeated in a meeting, and it would be
// wrong about as often as it was right.
func describeTrend(t *store.TrendSeries) {
	vals := make([]float64, 0, len(t.Points))
	for _, p := range t.Points {
		if p.AvailabilityPct != nil {
			vals = append(vals, *p.AvailabilityPct)
		}
	}
	if len(vals) == 0 {
		return
	}
	first, last := vals[0], vals[len(vals)-1]
	t.First, t.Last = &first, &last
	if len(vals) < 4 {
		return
	}
	half := len(vals) / 2
	mean := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs {
			sum += x
		}
		return sum / float64(len(xs))
	}
	early, late := mean(vals[:half]), mean(vals[half:])
	// A tenth of a percent is inside the noise of a partial day, so anything
	// smaller is reported as steady rather than as movement.
	var d string
	switch {
	case late-early > 0.1:
		d = "improving"
	case early-late > 0.1:
		d = "worsening"
	default:
		d = "steady"
	}
	t.Direction = &d
}

/* -------------------------------------------------------------------------- */
/* Bulk action                                                                 */
/* -------------------------------------------------------------------------- */

var bulkActions = map[string]bool{"suspend": true, "activate": true, "delete": true}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

func (s *Store) BulkAction(ctx context.Context, in store.BulkActionInput) (store.BulkActionResult, error) {
	res := store.BulkActionResult{
		Action: in.Action, Requested: len(in.ResourceIDs), Skipped: map[string]string{},
	}
	if !bulkActions[in.Action] {
		return res, invalid("action must be suspend, activate or delete")
	}
	if len(in.ResourceIDs) == 0 {
		return res, invalid("select at least one monitor")
	}
	if len(in.ResourceIDs) > 500 {
		return res, invalid("that is %d monitors; bulk actions are capped at 500 per "+
			"request so a mis-click cannot take out an estate", len(in.ResourceIDs))
	}
	// Checked here rather than left to PostgreSQL. A blank or malformed id reaches
	// the driver as an uuid cast failure, which surfaces as a 500 and tells the
	// caller nothing about which value was wrong.
	for _, id := range in.ResourceIDs {
		if !isUUID(id) {
			return res, invalid("%q is not a monitor id", id)
		}
	}

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// Discovered resources come back on the next collection, so deleting one is
		// futile and misleading. Only monitors this tool owns can be deleted.
		//
		// Read as a single row rather than by iterating a cursor: holding an open
		// Rows while issuing the UPDATE below aborts the transaction in pgx, which
		// is exactly what happened the first time this was written.
		if in.Action == "delete" {
			var names []string
			var ids []string
			if err := tx.QueryRow(ctx, `
				SELECT coalesce(array_agg(display_name), '{}'),
				       coalesce(array_agg(id::text), '{}')
				FROM resources
				 WHERE id = ANY($1::uuid[]) AND cloud_account_id IS NOT NULL
				   AND deleted_at IS NULL`, in.ResourceIDs).Scan(&names, &ids); err != nil {
				return err
			}
			blocked := make(map[string]bool, len(ids))
			for i, id := range ids {
				blocked[id] = true
				res.Skipped[names[i]] = "discovered from a cloud account; it would " +
					"reappear on the next collection. Suspend it instead."
			}
			keep := make([]string, 0, len(in.ResourceIDs))
			for _, id := range in.ResourceIDs {
				if !blocked[id] {
					keep = append(keep, id)
				}
			}
			in.ResourceIDs = keep
			if len(in.ResourceIDs) == 0 {
				return nil
			}
		}

		switch in.Action {
		case "suspend", "activate":
			suspended := in.Action == "suspend"
			status := "suspended"
			if !suspended {
				// Back to unknown, not up: nothing has measured it since, and
				// claiming it is up would be a statement nothing supports.
				status = "unknown"
			}
			t, err := tx.Exec(ctx, `
				UPDATE resources
				   SET suspended = $2, status = $3, status_since = now(), updated_at = now()
				 WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL AND suspended <> $2`,
				in.ResourceIDs, suspended, status)
			if err != nil {
				return err
			}
			res.Applied = int(t.RowsAffected())
		case "delete":
			t, err := tx.Exec(ctx, `
				UPDATE resources SET deleted_at = now(), updated_at = now()
				 WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL`, in.ResourceIDs)
			if err != nil {
				return err
			}
			res.Applied = int(t.RowsAffected())
		}
		return nil
	})
	if len(res.Skipped) == 0 {
		res.Skipped = nil
	}
	return res, err
}

/* -------------------------------------------------------------------------- */
/* Cloud inventory                                                             */
/* -------------------------------------------------------------------------- */

// CloudInventory answers "what is in this account, and what shape is it in".
//
// Deliberately reports monitored against discovered, not just monitored. A
// dashboard that counts only what it watches cannot tell you what it is missing,
// and on this tenancy the gap is the interesting part: fifteen thousand objects
// found, a hundred and fifty monitored, and eleven types that are neither watched
// nor deliberately ignored.
func (s *Store) CloudInventory(ctx context.Context, accountID string) (store.CloudInventory, error) {
	out := store.CloudInventory{
		AccountID: accountID,
		Types:     []store.InventoryType{},
		Regions:   []store.InventoryRegion{},
	}

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `
			SELECT display_name, provider, discovered_total, ignored_total,
			       last_discovery_at, unmapped_types
			FROM cloud_accounts WHERE id = $1::uuid`, accountID).
			Scan(&out.AccountName, &out.Provider, &out.Discovered, &out.Ignored,
				&out.LastRunAt, &raw); err != nil {
			if isNoRowsErr(err) {
				return ErrNotFound
			}
			return err
		}
		counts := map[string]int{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &counts)
		}
		for t, n := range counts {
			out.Unmapped = append(out.Unmapped, store.UnmappedType{ProviderType: t, Count: n})
			out.UnmappedTotal += n
		}
		sort.Slice(out.Unmapped, func(i, j int) bool {
			return out.Unmapped[i].Count > out.Unmapped[j].Count
		})

		// Footprint and health per type in one pass. Status counts sit beside the
		// total because forty resources with nine unknown is a different situation
		// from forty healthy ones, and a bare count hides that.
		rows, err := tx.Query(ctx, `
			SELECT r.resource_type,
			       count(*)::int,
			       coalesce(array_agg(DISTINCT r.region) FILTER (WHERE r.region IS NOT NULL), '{}'),
			       count(*) FILTER (WHERE r.status = 'up')::int,
			       count(*) FILTER (WHERE r.status = 'down')::int,
			       count(*) FILTER (WHERE r.status = 'trouble')::int,
			       count(*) FILTER (WHERE r.status = 'critical')::int,
			       count(*) FILTER (WHERE r.status = 'unknown')::int,
			       count(*) FILTER (WHERE r.status = 'suspended')::int
			FROM resources r
			WHERE r.cloud_account_id = $1::uuid AND r.deleted_at IS NULL
			GROUP BY r.resource_type`, accountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t store.InventoryType
			if err := rows.Scan(&t.Code, &t.Count, &t.Regions, &t.Up, &t.Down,
				&t.Trouble, &t.Critical, &t.Unknown, &t.Suspended); err != nil {
				return err
			}
			t.DisplayName, _ = typeMeta(t.Code)
			if ct, ok := catalog.Get(t.Code); ok {
				t.Category = ct.Category
			}
			out.Monitored += t.Count
			out.Types = append(out.Types, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		regions, err := tx.Query(ctx, `
			SELECT coalesce(r.region,'unknown'), count(*)::int,
			       count(DISTINCT r.resource_type)::int
			FROM resources r
			WHERE r.cloud_account_id = $1::uuid AND r.deleted_at IS NULL
			GROUP BY 1 ORDER BY 2 DESC`, accountID)
		if err != nil {
			return err
		}
		defer regions.Close()
		for regions.Next() {
			var rg store.InventoryRegion
			if err := regions.Scan(&rg.Region, &rg.Count, &rg.Types); err != nil {
				return err
			}
			out.Regions = append(out.Regions, rg)
		}
		return regions.Err()
	})
	if err != nil {
		return out, err
	}

	// Largest footprint first: an inventory is read to find where the estate is,
	// and alphabetical order buries that.
	sort.Slice(out.Types, func(i, j int) bool {
		if out.Types[i].Count != out.Types[j].Count {
			return out.Types[i].Count > out.Types[j].Count
		}
		return out.Types[i].DisplayName < out.Types[j].DisplayName
	})
	return out, nil
}

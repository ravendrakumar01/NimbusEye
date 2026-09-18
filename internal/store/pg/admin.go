package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/store"
)

// Administration queries.
//
// These write the configuration the alerter and collector read at runtime, so the
// validation here is the last line before a bad threshold silently stops alerting.
// Two invariants are enforced rather than trusted:
//
//   A rule may only name a metric its resource type actually defines. Otherwise a
//   typo produces a rule that is stored, displayed, and never evaluated — the
//   worst failure mode a monitoring tool has, because the screen says it is
//   covered.
//
//   Direction comes from the catalog. Free disk space and CPU utilisation are both
//   percentages, but 5 means trouble for one and nothing for the other. Letting a
//   profile declare its own direction means one wrong click inverts an alert.

var _ store.Administrator = (*Store)(nil)

// ErrNotFound is returned when an administered object does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalid carries a message safe to show the user.
type ErrInvalid struct{ Msg string }

func (e ErrInvalid) Error() string { return e.Msg }

// invalid builds a user-visible validation error.
//
// Deliberately a plain printf wrapper so `go vet` keeps checking every format
// string passed to it. That check earns its keep: it caught two messages here
// containing a literal percent sign, which Sprintf was reading as a broken verb.
// A literal percent must be written %% — which is mildly awkward, and much better
// than losing the analysis on every other call site.
func invalid(format string, a ...any) error {
	return ErrInvalid{Msg: fmt.Sprintf(format, a...)}
}

/* -------------------------------------------------------------------------- */
/* Threshold profiles                                                          */
/* -------------------------------------------------------------------------- */

// decorateRules fills the catalog-derived fields and drops rules whose metric the
// type no longer defines, so a stale rule is visible as absent rather than shown
// as active while being ignored by the evaluator.
func decorateRules(resourceType string, rules []store.ThresholdRule) []store.ThresholdRule {
	t, ok := catalog.Get(resourceType)
	if !ok {
		return rules
	}
	out := make([]store.ThresholdRule, 0, len(rules))
	for _, r := range rules {
		m, ok := t.Metric(r.Metric)
		if !ok {
			continue
		}
		r.Label, r.Unit, r.HigherIsWorse = m.Label, m.Unit, m.HigherIsWorse
		out = append(out, r)
	}
	return out
}

// unconfiguredMetrics lists metrics the type defines but the profile does not use,
// with the catalog defaults pre-filled so adding one is a single click.
func unconfiguredMetrics(resourceType string, rules []store.ThresholdRule) []store.ThresholdRule {
	t, ok := catalog.Get(resourceType)
	if !ok {
		return nil
	}
	used := make(map[string]bool, len(rules))
	for _, r := range rules {
		used[r.Metric] = true
	}
	var out []store.ThresholdRule
	for _, m := range t.Metrics {
		if used[m.Key] {
			continue
		}
		op := ">="
		if !m.HigherIsWorse {
			op = "<="
		}
		out = append(out, store.ThresholdRule{
			Metric: m.Key, Op: op, Trouble: m.Trouble, Critical: m.Critical,
			PollsCheck: 3, Strategy: "consecutive",
			Label: m.Label, Unit: m.Unit, HigherIsWorse: m.HigherIsWorse,
		})
	}
	return out
}

const thresholdSelect = `
	SELECT p.id::text, p.display_name, p.resource_type, p.rules, p.down_polls_check,
	       p.system_generated, p.is_default, p.updated_at,
	       (SELECT count(*) FROM resources r
	         WHERE r.resource_type = p.resource_type AND r.deleted_at IS NULL)::int
	FROM threshold_profiles p`

func scanThreshold(rows pgx.Rows) (store.ThresholdProfile, error) {
	var p store.ThresholdProfile
	var raw []byte
	if err := rows.Scan(&p.ID, &p.DisplayName, &p.ResourceType, &raw, &p.DownPollsCheck,
		&p.SystemGenerated, &p.IsDefault, &p.UpdatedAt, &p.ResourceCount); err != nil {
		return p, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Rules)
	}
	if p.Rules == nil {
		p.Rules = []store.ThresholdRule{}
	}
	p.Rules = decorateRules(p.ResourceType, p.Rules)
	if t, ok := catalog.Get(p.ResourceType); ok {
		p.Provider, p.TypeName = t.Provider, t.DisplayName
	}
	return p, nil
}

func (s *Store) ThresholdProfiles(ctx context.Context, provider []string) ([]store.ThresholdProfile, error) {
	out := []store.ThresholdProfile{}
	var args []any
	where := ""
	if codes := codesFor(provider, nil); codes != nil {
		args = append(args, codes)
		where = " WHERE p.resource_type = ANY($1)"
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, thresholdSelect+where+" ORDER BY p.display_name", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanThreshold(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) ThresholdProfile(ctx context.Context, id string) (store.ThresholdProfile, error) {
	var p store.ThresholdProfile
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, thresholdSelect+" WHERE p.id = $1::uuid", id)
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
		p, err = scanThreshold(rows)
		return err
	})
	if err != nil {
		return p, err
	}
	p.AvailableMetrics = unconfiguredMetrics(p.ResourceType, p.Rules)
	return p, nil
}

// validateRules rejects anything the evaluator would silently ignore or misread.
func validateRules(resourceType string, rules []store.ThresholdRule) ([]store.ThresholdRule, error) {
	t, ok := catalog.Get(resourceType)
	if !ok {
		return nil, invalid("unknown resource type %q", resourceType)
	}
	seen := map[string]bool{}
	out := make([]store.ThresholdRule, 0, len(rules))
	for _, r := range rules {
		m, ok := t.Metric(r.Metric)
		if !ok {
			return nil, invalid("%s has no metric %q", t.DisplayName, r.Metric)
		}
		if seen[r.Metric] {
			return nil, invalid("metric %q appears twice; a metric can have only one rule", r.Metric)
		}
		seen[r.Metric] = true

		if r.Trouble == nil && r.Critical == nil {
			return nil, invalid("%s needs at least one of trouble or critical, otherwise the rule does nothing", m.Label)
		}
		if r.PollsCheck < 1 || r.PollsCheck > 60 {
			return nil, invalid("%s: polls to confirm must be between 1 and 60", m.Label)
		}
		switch r.Strategy {
		case "", "consecutive":
			r.Strategy = "consecutive"
		case "average":
		default:
			return nil, invalid("%s: strategy must be consecutive or average", m.Label)
		}

		// Direction is the catalog's, not the caller's. This is what stops a
		// free-space alert from being inverted into one that never fires.
		if m.HigherIsWorse {
			r.Op = ">="
			if r.Trouble != nil && r.Critical != nil && *r.Critical < *r.Trouble {
				return nil, invalid("%s: critical (%g) must be at or above trouble (%g) for a metric where higher is worse",
					m.Label, *r.Critical, *r.Trouble)
			}
		} else {
			r.Op = "<="
			if r.Trouble != nil && r.Critical != nil && *r.Critical > *r.Trouble {
				return nil, invalid("%s: critical (%g) must be at or below trouble (%g) for a metric where lower is worse",
					m.Label, *r.Critical, *r.Trouble)
			}
		}

		// Stored form carries only the authoritative fields; the labels are
		// re-derived on read so a catalog change does not leave stale text behind.
		out = append(out, store.ThresholdRule{
			Metric: r.Metric, Op: r.Op, Trouble: r.Trouble, Critical: r.Critical,
			PollsCheck: r.PollsCheck, Strategy: r.Strategy,
		})
	}
	return out, nil
}

func (s *Store) UpdateThresholdProfile(ctx context.Context, id string, u store.ThresholdProfileUpdate) (store.ThresholdProfile, error) {
	cur, err := s.ThresholdProfile(ctx, id)
	if err != nil {
		return cur, err
	}

	rules := cur.Rules
	if u.Rules != nil {
		rules, err = validateRules(cur.ResourceType, u.Rules)
		if err != nil {
			return cur, err
		}
	}
	polls := cur.DownPollsCheck
	if u.DownPollsCheck != nil {
		if *u.DownPollsCheck < 1 || *u.DownPollsCheck > 60 {
			return cur, invalid("failed polls before down must be between 1 and 60")
		}
		polls = *u.DownPollsCheck
	}

	raw, err := json.Marshal(rules)
	if err != nil {
		return cur, err
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE threshold_profiles
			   SET rules = $2::jsonb, down_polls_check = $3, updated_at = now()
			 WHERE id = $1::uuid`, id, raw, polls)
		return err
	})
	if err != nil {
		return cur, err
	}
	return s.ThresholdProfile(ctx, id)
}

/* -------------------------------------------------------------------------- */
/* Notification profiles                                                       */
/* -------------------------------------------------------------------------- */

func (s *Store) NotificationProfiles(ctx context.Context) ([]store.NotificationProfile, error) {
	out := []store.NotificationProfile{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id::text, p.display_name, p.notification_delay,
			       p.business_hours_id::text, coalesce(b.display_name,''),
			       p.notify_outside_business_hours, p.alert_rules, p.escalation_levels,
			       p.persistent_alert_interval, p.notify_on_recovery, p.rca_needed,
			       p.is_default, p.updated_at
			FROM notification_profiles p
			LEFT JOIN business_hours b ON b.id = p.business_hours_id
			ORDER BY p.is_default DESC, p.display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p store.NotificationProfile
			var rulesRaw, escRaw []byte
			if err := rows.Scan(&p.ID, &p.DisplayName, &p.NotificationDelay,
				&p.BusinessHoursID, &p.BusinessHoursName, &p.NotifyOutsideBusinessHours,
				&rulesRaw, &escRaw, &p.PersistentAlertInterval, &p.NotifyOnRecovery,
				&p.RCANeeded, &p.IsDefault, &p.UpdatedAt); err != nil {
				return err
			}
			p.AlertRules = []store.AlertRule{}
			p.EscalationLevels = []store.EscalationLevel{}
			if len(rulesRaw) > 0 {
				_ = json.Unmarshal(rulesRaw, &p.AlertRules)
			}
			if len(escRaw) > 0 {
				_ = json.Unmarshal(escRaw, &p.EscalationLevels)
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// knownChannels returns the set of channel ids, used to reject routing to a
// channel that does not exist — which would otherwise be a profile that looks
// configured and notifies nobody.
func (s *Store) knownChannels(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM notification_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

var validSeverities = map[string]bool{"down": true, "critical": true, "trouble": true, "up": true}

func (s *Store) UpdateNotificationProfile(ctx context.Context, id string, u store.NotificationProfileUpdate) (store.NotificationProfile, error) {
	var out store.NotificationProfile

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		known, err := s.knownChannels(ctx, tx)
		if err != nil {
			return err
		}

		sets := []string{"updated_at = now()"}
		args := []any{id}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}

		if u.DisplayName != nil {
			name := strings.TrimSpace(*u.DisplayName)
			if name == "" {
				return invalid("name cannot be empty")
			}
			add("display_name", name)
		}
		if u.NotificationDelay != nil {
			if *u.NotificationDelay < 0 || *u.NotificationDelay > 60 {
				return invalid("notification delay must be between 0 and 60 polls")
			}
			add("notification_delay", *u.NotificationDelay)
		}
		if u.PersistentAlertInterval != nil {
			if *u.PersistentAlertInterval < 0 || *u.PersistentAlertInterval > 1440 {
				return invalid("repeat interval must be between 0 and 1440 minutes")
			}
			add("persistent_alert_interval", *u.PersistentAlertInterval)
		}
		if u.NotifyOnRecovery != nil {
			add("notify_on_recovery", *u.NotifyOnRecovery)
		}
		if u.RCANeeded != nil {
			add("rca_needed", *u.RCANeeded)
		}

		if u.AlertRules != nil {
			for _, r := range *u.AlertRules {
				if !validSeverities[r.Severity] {
					return invalid("unknown severity %q", r.Severity)
				}
				for _, c := range r.Channels {
					if !known[c] {
						return invalid("severity %s routes to a channel that no longer exists", r.Severity)
					}
				}
			}
			raw, err := json.Marshal(*u.AlertRules)
			if err != nil {
				return err
			}
			add("alert_rules", raw)
		}
		if u.EscalationLevels != nil {
			levels := *u.EscalationLevels
			sort.Slice(levels, func(i, j int) bool { return levels[i].AfterMinutes < levels[j].AfterMinutes })
			prev := -1
			for i := range levels {
				if levels[i].AfterMinutes <= 0 {
					return invalid("an escalation level must wait at least a minute")
				}
				if levels[i].AfterMinutes == prev {
					return invalid("two escalation levels cannot fire at the same minute")
				}
				prev = levels[i].AfterMinutes
				for _, c := range levels[i].Channels {
					if !known[c] {
						return invalid("escalation level %d routes to a channel that no longer exists", i+1)
					}
				}
				// Levels are renumbered from their order in time, so the stored
				// level number always matches the sequence that will actually run.
				levels[i].Level = i + 1
			}
			raw, err := json.Marshal(levels)
			if err != nil {
				return err
			}
			add("escalation_levels", raw)
		}

		if len(sets) == 1 {
			return invalid("nothing to change")
		}
		tag, err := tx.Exec(ctx, `UPDATE notification_profiles SET `+
			strings.Join(sets, ", ")+` WHERE id = $1::uuid`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return out, err
	}

	all, err := s.NotificationProfiles(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return out, ErrNotFound
}

/* -------------------------------------------------------------------------- */
/* Notification channels                                                       */
/* -------------------------------------------------------------------------- */

var channelTypes = map[string]bool{
	"email": true, "sms": true, "voice": true, "slack": true,
	"teams": true, "webhook": true, "pagerduty": true, "push": true,
}

// channelsNeedingSecret are the types whose configuration is inherently a secret:
// the URL or token *is* the credential. Storing it in the config column would put
// it in every database dump.
var channelsNeedingSecret = map[string]bool{
	"slack": true, "teams": true, "webhook": true, "pagerduty": true,
}

func validateChannel(in store.ChannelInput) (store.ChannelInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.ChannelType = strings.ToLower(strings.TrimSpace(in.ChannelType))
	in.SecretRef = strings.TrimSpace(in.SecretRef)

	if in.DisplayName == "" {
		return in, invalid("give the channel a name")
	}
	if !channelTypes[in.ChannelType] {
		return in, invalid("unsupported channel type %q", in.ChannelType)
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}

	// Reject anything that looks like a secret arriving in the config. The caller
	// is meant to place it in a file and pass the path.
	for k, v := range in.Config {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "key") {
			return in, invalid("%q looks like a secret; write it to a file on the server and set secret_ref to that path instead", k)
		}
		if sv, ok := v.(string); ok && strings.Contains(strings.ToLower(sv), "hooks.slack.com") {
			return in, invalid("a webhook URL is a credential; put it in a file and reference it with secret_ref")
		}
	}

	switch in.ChannelType {
	case "email":
		addr, _ := in.Config["address"].(string)
		if !strings.Contains(addr, "@") {
			return in, invalid("an email channel needs a config address")
		}
	case "sms", "voice":
		if p, _ := in.Config["phone"].(string); strings.TrimSpace(p) == "" {
			return in, invalid("a %s channel needs a config phone number", in.ChannelType)
		}
	}
	if channelsNeedingSecret[in.ChannelType] && in.SecretRef == "" {
		return in, invalid("a %s channel needs secret_ref: the server-side path holding its URL or token", in.ChannelType)
	}
	if in.SecretRef != "" && !strings.HasPrefix(in.SecretRef, "/") {
		return in, invalid("secret_ref must be an absolute path on the server")
	}
	return in, nil
}

const channelSelect = `
	SELECT c.id::text, c.channel_type, c.display_name, c.config,
	       coalesce(c.secret_ref,''), c.enabled, c.verified_at, c.created_at,
	       (SELECT count(*) FROM notification_profiles p
	         WHERE p.alert_rules::text LIKE '%' || c.id::text || '%'
	            OR p.escalation_levels::text LIKE '%' || c.id::text || '%')::int
	FROM notification_channels c`

func scanChannel(rows pgx.Rows) (store.NotificationChannel, error) {
	var c store.NotificationChannel
	var raw []byte
	if err := rows.Scan(&c.ID, &c.ChannelType, &c.DisplayName, &raw, &c.SecretRef,
		&c.Enabled, &c.VerifiedAt, &c.CreatedAt, &c.UsedBy); err != nil {
		return c, err
	}
	c.Config = map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.Config)
	}
	return c, nil
}

func (s *Store) NotificationChannels(ctx context.Context) ([]store.NotificationChannel, error) {
	out := []store.NotificationChannel{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, channelSelect+" ORDER BY c.display_name")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanChannel(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) channelByID(ctx context.Context, id string) (store.NotificationChannel, error) {
	var c store.NotificationChannel
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, channelSelect+" WHERE c.id = $1::uuid", id)
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
		c, err = scanChannel(rows)
		return err
	})
	return c, err
}

func (s *Store) CreateChannel(ctx context.Context, in store.ChannelInput) (store.NotificationChannel, error) {
	var out store.NotificationChannel
	in, err := validateChannel(in)
	if err != nil {
		return out, err
	}
	cfg, err := json.Marshal(in.Config)
	if err != nil {
		return out, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var id string
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var secret *string
		if in.SecretRef != "" {
			secret = &in.SecretRef
		}
		return tx.QueryRow(ctx, `
			INSERT INTO notification_channels
			  (tenant_id, channel_type, display_name, config, secret_ref, enabled)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, $2, $3::jsonb, $4, $5)
			RETURNING id::text`,
			in.ChannelType, in.DisplayName, cfg, secret, enabled).Scan(&id)
	})
	if err != nil {
		if strings.Contains(err.Error(), "notification_channels_tenant_id_display_name_key") {
			return out, invalid("a channel named %q already exists", in.DisplayName)
		}
		return out, err
	}
	return s.channelByID(ctx, id)
}

func (s *Store) UpdateChannel(ctx context.Context, id string, in store.ChannelInput) (store.NotificationChannel, error) {
	cur, err := s.channelByID(ctx, id)
	if err != nil {
		return cur, err
	}
	// Type is immutable: the config shape depends on it, and changing it in place
	// would leave a Slack channel holding an email address.
	if in.ChannelType != "" && in.ChannelType != cur.ChannelType {
		return cur, invalid("a channel's type cannot be changed; delete it and create the new one")
	}
	in.ChannelType = cur.ChannelType
	if in.DisplayName == "" {
		in.DisplayName = cur.DisplayName
	}
	if in.Config == nil {
		in.Config = cur.Config
	}
	if in.SecretRef == "" {
		in.SecretRef = cur.SecretRef
	}
	in, err = validateChannel(in)
	if err != nil {
		return cur, err
	}
	cfg, err := json.Marshal(in.Config)
	if err != nil {
		return cur, err
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var secret *string
		if in.SecretRef != "" {
			secret = &in.SecretRef
		}
		// Changing where a secret lives invalidates a previous verification.
		_, err := tx.Exec(ctx, `
			UPDATE notification_channels
			   SET display_name = $2, config = $3::jsonb, secret_ref = $4, enabled = $5,
			       verified_at = CASE WHEN coalesce(secret_ref,'') = coalesce($4,'')
			                          THEN verified_at ELSE NULL END
			 WHERE id = $1::uuid`, id, in.DisplayName, cfg, secret, enabled)
		return err
	})
	if err != nil {
		return cur, err
	}
	return s.channelByID(ctx, id)
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	c, err := s.channelByID(ctx, id)
	if err != nil {
		return err
	}
	// Deleting a channel a profile routes to would silently stop those
	// notifications, so it is refused while it is referenced.
	if c.UsedBy > 0 {
		return invalid("%q is used by %d notification profile(s); remove it from them first",
			c.DisplayName, c.UsedBy)
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1::uuid`, id)
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
/* Users                                                                       */
/* -------------------------------------------------------------------------- */

const userSelect = `
	SELECT id::text, email::text, display_name, role, status, mfa_enabled,
	       coalesce(timezone,''), last_login_at, created_at,
	       password_hash IS NOT NULL, locked_until, failed_logins
	FROM users`

func scanUser(rows pgx.Rows) (store.AdminUser, error) {
	var u store.AdminUser
	err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Status, &u.MFAEnabled,
		&u.Timezone, &u.LastLoginAt, &u.CreatedAt, &u.HasPassword, &u.LockedUntil, &u.FailedLogins)
	return u, err
}

func (s *Store) AdminUsers(ctx context.Context) ([]store.AdminUser, error) {
	out := []store.AdminUser{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, userSelect+` ORDER BY
			CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1
			          WHEN 'operator' THEN 2 ELSE 3 END, display_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) userByID(ctx context.Context, id string) (store.AdminUser, error) {
	var u store.AdminUser
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, userSelect+" WHERE id = $1::uuid", id)
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
		u, err = scanUser(rows)
		return err
	})
	return u, err
}

var validRoles = map[string]bool{"owner": true, "admin": true, "operator": true, "viewer": true}

func (s *Store) CreateUser(ctx context.Context, in store.UserInput) (store.AdminUser, error) {
	var out store.AdminUser
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name := strings.TrimSpace(in.DisplayName)
	if !strings.Contains(email, "@") || len(email) < 5 {
		return out, invalid("enter a valid email address")
	}
	if name == "" {
		return out, invalid("enter a name")
	}
	if in.Role == "" {
		in.Role = "viewer"
	}
	if !validRoles[in.Role] {
		return out, invalid("unknown role %q", in.Role)
	}

	var id string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// Status is 'invited' and password_hash stays NULL. A user is created
		// without a password and sets their own through the reset flow; an
		// administrator choosing someone else's password means it has been
		// transmitted in plain text somewhere.
		return tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, display_name, role, status, timezone)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1, $2, $3, 'invited',
			        nullif($4,''))
			RETURNING id::text`, email, name, in.Role, strings.TrimSpace(in.Timezone)).Scan(&id)
	})
	if err != nil {
		if strings.Contains(err.Error(), "users_tenant_id_email_key") {
			return out, invalid("%s is already a user here", email)
		}
		return out, err
	}
	return s.userByID(ctx, id)
}

func (s *Store) UpdateUser(ctx context.Context, id string, in store.UserInput) (store.AdminUser, error) {
	cur, err := s.userByID(ctx, id)
	if err != nil {
		return cur, err
	}

	err = s.withTx(ctx, func(tx pgx.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{id}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}

		if n := strings.TrimSpace(in.DisplayName); n != "" && n != cur.DisplayName {
			add("display_name", n)
		}
		if in.Role != "" && in.Role != cur.Role {
			if !validRoles[in.Role] {
				return invalid("unknown role %q", in.Role)
			}
			// The last owner cannot be demoted. Without this the tenant can be
			// left with nobody able to grant roles back, and the only fix is
			// direct database access.
			if cur.Role == "owner" && in.Role != "owner" {
				var owners int
				if err := tx.QueryRow(ctx,
					`SELECT count(*)::int FROM users WHERE role = 'owner' AND status <> 'disabled'`).
					Scan(&owners); err != nil {
					return err
				}
				if owners <= 1 {
					return invalid("this is the only owner; promote someone else to owner first")
				}
			}
			add("role", in.Role)
		}
		if in.Status != nil && *in.Status != cur.Status {
			st := *in.Status
			if st != "active" && st != "disabled" && st != "invited" {
				return invalid("unknown status %q", st)
			}
			if st == "active" && !cur.HasPassword {
				return invalid("%s has not set a password yet, so they cannot be activated; send them a reset link", cur.Email)
			}
			if st == "disabled" && cur.Role == "owner" {
				var owners int
				if err := tx.QueryRow(ctx,
					`SELECT count(*)::int FROM users WHERE role = 'owner' AND status <> 'disabled'`).
					Scan(&owners); err != nil {
					return err
				}
				if owners <= 1 {
					return invalid("this is the only active owner; you would lock everyone out")
				}
			}
			add("status", st)
			// Disabling someone must end their current access, not merely prevent
			// the next login.
			if st == "disabled" {
				if _, err := tx.Exec(ctx,
					`DELETE FROM sessions WHERE user_id = $1::uuid`, id); err != nil {
					return err
				}
			}
		}
		if tz := strings.TrimSpace(in.Timezone); tz != "" && tz != cur.Timezone {
			add("timezone", tz)
		}
		if len(sets) == 1 {
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+
			` WHERE id = $1::uuid`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	return s.userByID(ctx, id)
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	u, err := s.userByID(ctx, id)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if u.Role == "owner" {
			var owners int
			if err := tx.QueryRow(ctx,
				`SELECT count(*)::int FROM users WHERE role = 'owner'`).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return invalid("this is the only owner; the tenant would have no administrator")
			}
		}
		// The audit log keeps the row's history via ON DELETE SET NULL, so past
		// actions remain readable as "a deleted user" rather than vanishing.
		tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) UnlockUser(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE users SET failed_logins = 0, locked_until = NULL, updated_at = now()
			 WHERE id = $1::uuid`, id)
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
/* Audit log                                                                   */
/* -------------------------------------------------------------------------- */

func (s *Store) AuditLog(ctx context.Context, f store.AuditFilter) (store.AuditPage, error) {
	out := store.AuditPage{Entries: []store.AuditEntry{}}
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
	if f.Action != "" {
		conds = append(conds, "a.action = "+next(f.Action))
	}
	if f.UserID != "" {
		conds = append(conds, "a.user_id = "+next(f.UserID)+"::uuid")
	}
	if f.Since != nil {
		conds = append(conds, "a.created_at >= "+next(*f.Since))
	}
	// Keyset pagination on the primary key. An offset would skip or repeat rows,
	// because the log grows while it is being read.
	if f.Before > 0 {
		conds = append(conds, "a.id < "+next(f.Before))
	}
	where := strings.Join(conds, " AND ")

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.user_id::text, coalesce(u.email::text,''), a.action,
			       coalesce(a.object_type,''), coalesce(a.object_id,''), a.detail,
			       coalesce(host(a.ip),''), a.created_at
			FROM audit_log a
			LEFT JOIN users u ON u.id = a.user_id
			WHERE `+where+`
			ORDER BY a.id DESC LIMIT `+fmt.Sprint(limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e store.AuditEntry
			var raw []byte
			if err := rows.Scan(&e.ID, &e.UserID, &e.UserEmail, &e.Action,
				&e.ObjectType, &e.ObjectID, &raw, &e.IP, &e.CreatedAt); err != nil {
				return err
			}
			e.Detail = map[string]any{}
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &e.Detail)
			}
			out.Entries = append(out.Entries, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// One row beyond the page proves there is a next page without a count.
		if len(out.Entries) > limit {
			out.Entries = out.Entries[:limit]
			out.NextBefore = out.Entries[limit-1].ID
		}

		acts, err := tx.Query(ctx, `
			SELECT DISTINCT action FROM audit_log
			WHERE created_at > now() - interval '90 days' ORDER BY action`)
		if err != nil {
			return err
		}
		defer acts.Close()
		for acts.Next() {
			var a string
			if err := acts.Scan(&a); err != nil {
				return err
			}
			out.Actions = append(out.Actions, a)
		}
		return acts.Err()
	})
	return out, err
}

// WriteAudit records an administrative action. Failure to record is logged but
// does not fail the action: refusing a legitimate change because the audit insert
// failed would be worse than a gap in the log, which is itself visible.
// validIP returns the address only when PostgreSQL will accept it as inet.
func validIP(ip string) *string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	// A host:port pair reaches here whenever a caller passes RemoteAddr straight
	// through; take the host rather than discarding the address.
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if net.ParseIP(ip) == nil {
		return nil
	}
	return &ip
}

func (s *Store) WriteAudit(ctx context.Context, userID, action, objType, objID, ip string, detail map[string]any) {
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	s.read(ctx, "audit write", func(tx pgx.Tx) error {
		var uid *string
		if userID != "" {
			uid = &userID
		}
		// A malformed address must not cost us the entry. Which action was taken,
		// by whom, matters more than where from, so an unusable IP degrades to
		// NULL rather than failing the insert.
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log
			  (tenant_id, user_id, action, object_type, object_id, detail, ip)
			VALUES (current_setting('nimbuseye.tenant_id')::uuid, $1::uuid, $2, $3, $4,
			        $5::jsonb, $6::inet)`,
			uid, action, nullIfEmpty(objType), nullIfEmpty(objID), raw, validIP(ip))
		return err
	})
}

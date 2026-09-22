package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notification routing and delivery.
//
// Deciding who to tell and actually telling them are separate steps on purpose.
// The decision writes a ledger row; the send reads it. That means a delivery
// failure is recorded against a specific intent rather than lost, and "was anyone
// told about this" stays answerable after the fact — which is the only question
// anybody asks of an alerting system once something has gone wrong twice.
//
// The spam problem is the hard part, not the sending. Four independent brakes:
//
//	One row per alert per escalation level, enforced by a uniqueness check. An
//	alert that stays open notifies at level 0, then 1, then 2, and then stops.
//
//	notification_delay: the alert must have been confirmed this many polls before
//	anyone hears about it.
//
//	persistent_alert_interval: repeats are off unless explicitly enabled, and when
//	enabled they are measured from last_notified_at.
//
//	muted_until and maintenance windows, which suppress the message without hiding
//	the problem.

// alertRule mirrors the JSON in notification_profiles.alert_rules.
type alertRule struct {
	Severity string   `json:"severity"`
	Channels []string `json:"channels"`
}

// escalationLevel mirrors notification_profiles.escalation_levels.
type escalationLevel struct {
	Level        int      `json:"level"`
	AfterMinutes int      `json:"after_minutes"`
	Channels     []string `json:"channels"`
}

// route is a resolved decision for one alert: who to tell, or why not.
type route struct {
	alertID  string
	level    int
	channels []string
	// reason is set when the alert cannot be routed. Recorded verbatim so the
	// Alert Logs page explains the silence instead of implying the alert was
	// unimportant.
	reason string
}

// resolveRoutes decides, for every open alert that has no notification at its
// current level, which channels should hear about it.
//
// Done in Go rather than SQL because the decision involves JSON routing rules,
// timezone-aware business hours and several profile flags. Expressing that as one
// statement would produce a query nobody could change safely.
func (e *Evaluator) resolveRoutes(ctx context.Context, tx pgx.Tx) ([]route, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.severity, a.escalation_level, a.poll_count,
		       a.opened_at, a.last_notified_at, a.muted_until,
		       np.id::text, coalesce(np.notification_delay,1),
		       coalesce(np.persistent_alert_interval,0),
		       np.alert_rules, np.escalation_levels,
		       np.business_hours_id::text,
		       coalesce(np.notify_outside_business_hours, true),
		       coalesce(bh.timezone,''), coalesce(bh.time_config,'[]'::jsonb)
		FROM alerts a
		JOIN resources r ON r.id = a.resource_id
		LEFT JOIN notification_profiles np
		       ON np.id = coalesce(r.notification_profile_id,
		                           (SELECT id FROM notification_profiles
		                             WHERE is_default LIMIT 1))
		LEFT JOIN business_hours bh ON bh.id = np.business_hours_id
		WHERE a.state = 'open' AND a.resolved_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []route
	for rows.Next() {
		var (
			id, severity, profileID, tz string
			level, pollCount, delay     int
			repeatMin                   int
			openedAt                    time.Time
			lastNotified, mutedUntil    *time.Time
			rulesRaw, escRaw, hoursRaw  []byte
			bhID                        *string
			notifyOutside               bool
		)
		if err := rows.Scan(&id, &severity, &level, &pollCount, &openedAt,
			&lastNotified, &mutedUntil, &profileID, &delay, &repeatMin,
			&rulesRaw, &escRaw, &bhID, &notifyOutside, &tz, &hoursRaw); err != nil {
			return nil, err
		}

		r := route{alertID: id, level: level}

		switch {
		case profileID == "":
			r.reason = "no notification profile applies to this monitor"
		case mutedUntil != nil && mutedUntil.After(time.Now()):
			r.reason = fmt.Sprintf("muted until %s", mutedUntil.Format(time.RFC3339))
		case pollCount < delay:
			r.reason = fmt.Sprintf(
				"waiting for confirmation: %d of %d polls", pollCount, delay)
		}

		// Business hours gate the message, not the detection. Outside hours with
		// notify_outside off, the alert is still recorded and still escalates; it
		// simply waits. A down alert is not exempted here because the profile is
		// where that policy belongs — if somebody wants to be woken for down and
		// not for trouble, that is two profiles.
		if r.reason == "" && bhID != nil && !notifyOutside {
			var slots []struct {
				Day   int    `json:"day"`
				Start string `json:"start"`
				End   string `json:"end"`
			}
			_ = json.Unmarshal(hoursRaw, &slots)
			loc, lerr := time.LoadLocation(tz)
			if lerr != nil {
				loc = time.UTC
			}
			now := time.Now().In(loc)
			day := int(now.Weekday())
			if day == 0 {
				day = 7
			}
			cur := now.Format("15:04")
			inside := false
			for _, s := range slots {
				if s.Day == day && s.Start <= cur && cur < s.End {
					inside = true
					break
				}
			}
			if !inside {
				r.reason = "outside business hours, and this profile does not notify then"
			}
		}

		if r.reason == "" {
			// Escalation levels above zero may route somewhere different — that is
			// the point of escalating. Fall back to the severity rules when the
			// level has no channels of its own.
			if level > 0 {
				var levels []escalationLevel
				_ = json.Unmarshal(escRaw, &levels)
				for _, l := range levels {
					if l.Level == level && len(l.Channels) > 0 {
						r.channels = l.Channels
						break
					}
				}
			}
			if len(r.channels) == 0 {
				var rules []alertRule
				_ = json.Unmarshal(rulesRaw, &rules)
				for _, ru := range rules {
					if ru.Severity == severity {
						r.channels = ru.Channels
						break
					}
				}
			}
			if len(r.channels) == 0 {
				r.reason = fmt.Sprintf(
					"no channel is routed for %s severity in this notification profile", severity)
			}
		}

		// Repeats. Off unless the profile enables them, and measured from the last
		// notification so enabling it does not immediately fire for every alert.
		if r.reason == "" && lastNotified != nil && repeatMin > 0 {
			if time.Since(*lastNotified) < time.Duration(repeatMin)*time.Minute {
				continue
			}
		}

		out = append(out, r)
	}
	return out, rows.Err()
}

// queueNotifications writes delivery intents, one per alert per escalation level.
//
// Replaces an earlier version that wrote a hardcoded "no notification channel
// configured" for everything. Rows previously skipped for exactly that reason are
// re-queued once a channel exists: the intent was always to tell somebody, it
// failed for a configuration reason, and the configuration has since been fixed.
// Without that, the alerts already open when delivery is switched on would never
// notify, and the feature would look broken on the day it started working.
func (e *Evaluator) queueNotifications(ctx context.Context, tx pgx.Tx) (int, error) {
	routes, err := e.resolveRoutes(ctx, tx)
	if err != nil {
		return 0, err
	}

	queued := 0
	for _, r := range routes {
		// One row per alert per level. Checked rather than relied upon via a
		// constraint because a skipped row must be replaceable by a real one.
		var existingID, existingState, existingReason string
		err := tx.QueryRow(ctx, `
			SELECT id::text, state, coalesce(last_error,'')
			FROM alert_notifications
			WHERE alert_id = $1::uuid AND level = $2
			ORDER BY id DESC LIMIT 1`, r.alertID, r.level).
			Scan(&existingID, &existingState, &existingReason)

		switch {
		case err == nil:
			// Already handled at this level. The one exception is a row skipped
			// because nothing was configured, now that something is.
			stale := existingState == "skipped" &&
				(strings.Contains(existingReason, "no notification channel") ||
					strings.Contains(existingReason, "no channel is routed"))
			if !stale || len(r.channels) == 0 {
				continue
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM alert_notifications WHERE id = $1`, existingID); err != nil {
				return queued, err
			}
		case !isNoRows(err):
			return queued, err
		}

		if len(r.channels) == 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO alert_notifications
				  (tenant_id, alert_id, channel_id, level, state, last_error)
				VALUES (current_tenant_id(), $1::uuid, NULL, $2, 'skipped', $3)`,
				r.alertID, r.level, r.reason); err != nil {
				return queued, err
			}
			continue
		}

		for _, ch := range r.channels {
			// A disabled channel is not an error, it is a decision somebody made.
			// Recorded as skipped with that reason so the log does not imply the
			// message went out.
			var enabled bool
			var recipient *string
			if err := tx.QueryRow(ctx, `
				SELECT c.enabled, c.config->>'address'
				FROM notification_channels c WHERE c.id = $1::uuid`, ch).
				Scan(&enabled, &recipient); err != nil {
				if isNoRows(err) {
					continue
				}
				return queued, err
			}
			state, reason := "pending", ""
			if !enabled {
				state, reason = "skipped", "channel is disabled"
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO alert_notifications
				  (tenant_id, alert_id, channel_id, recipient, level, state, last_error)
				VALUES (current_tenant_id(), $1::uuid, $2::uuid, $3, $4, $5, nullif($6,''))`,
				r.alertID, ch, recipient, r.level, state, reason); err != nil {
				return queued, err
			}
			queued++
		}

		if _, err := tx.Exec(ctx,
			`UPDATE alerts SET last_notified_at = now() WHERE id = $1::uuid`,
			r.alertID); err != nil {
			return queued, err
		}
	}
	return queued, nil
}

// queueRecoveries tells the same people when a problem goes away.
//
// Without this an operator learns that something broke and never learns that it
// came back, which trains them to ignore the alerts: if you have to log in to find
// out whether it is still broken, the message added nothing.
func (e *Evaluator) queueRecoveries(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT a.id::text, n.channel_id::text, n.recipient
		FROM alerts a
		JOIN alert_notifications n ON n.alert_id = a.id AND n.state = 'sent'
		JOIN resources r ON r.id = a.resource_id
		LEFT JOIN notification_profiles np
		       ON np.id = coalesce(r.notification_profile_id,
		                           (SELECT id FROM notification_profiles
		                             WHERE is_default LIMIT 1))
		WHERE a.state = 'resolved'
		  AND a.resolved_at > now() - interval '1 hour'
		  AND coalesce(np.notify_on_recovery, true)
		  -- Level -1 marks the recovery message, so it cannot collide with the
		  -- escalation levels and is queued at most once.
		  AND NOT EXISTS (
		    SELECT 1 FROM alert_notifications x
		    WHERE x.alert_id = a.id AND x.level = -1)`)
	if err != nil {
		return 0, err
	}
	type rec struct {
		alertID, channelID string
		recipient          *string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.alertID, &r.channelID, &r.recipient); err != nil {
			rows.Close()
			return 0, err
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range recs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO alert_notifications
			  (tenant_id, alert_id, channel_id, recipient, level, state)
			VALUES (current_tenant_id(), $1::uuid, $2::uuid, $3, -1, 'pending')`,
			r.alertID, r.channelID, r.recipient); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func isNoRows(err error) bool {
	return err != nil && strings.Contains(err.Error(), pgx.ErrNoRows.Error())
}

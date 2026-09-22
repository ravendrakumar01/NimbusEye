package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Alarm actions.
//
// Each of these changes what the alerter will do on its next pass, so they are
// written where the evaluator reads them rather than kept in a parallel table. A
// mute the evaluator does not see is not a mute.

// alarmByID re-reads an alarm after a change, so the caller always gets the state
// that was actually persisted rather than the state it hoped for.
func (s *Store) alarmByID(ctx context.Context, id string) (model.Alarm, error) {
	var a model.Alarm
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var rcaRaw []byte
		var mw *string
		err := tx.QueryRow(ctx, `
			SELECT a.id::text, a.resource_id::text, r.display_name, r.resource_type,
			       coalesce(r.region,''), a.dedup_key, a.severity, a.state,
			       coalesce(a.metric_key,''), a.observed_value, a.threshold_value,
			       coalesce(a.message,''), a.poll_count, a.opened_at,
			       a.acknowledged_at, coalesce(a.acknowledged_by::text,''),
			       a.resolved_at, a.escalation_level,
			       a.muted_until, a.suppressed_by_maintenance::text, a.rca,
			       a.last_notified_at,
			       (SELECT count(*) FROM alert_notifications n
			         WHERE n.alert_id = a.id AND n.state = 'sent')::int
			FROM alerts a JOIN resources r ON r.id = a.resource_id
			WHERE a.id = $1::uuid`, id).
			Scan(&a.ID, &a.ResourceID, &a.ResourceName, &a.ResourceType, &a.Region,
				&a.DedupKey, &a.Severity, &a.State, &a.MetricKey, &a.ObservedValue,
				&a.ThresholdValue, &a.Message, &a.PollCount, &a.OpenedAt,
				&a.AckedAt, &a.AckedBy, &a.ResolvedAt, &a.EscalationLvl,
				&a.MutedUntil, &mw, &rcaRaw, &a.LastNotifiedAt, &a.Notified)
		if err != nil {
			if isNoRowsErr(err) {
				return ErrNotFound
			}
			return err
		}
		if mw != nil {
			a.SuppressedBy = *mw
		}
		a.RCA, a.MuteReason = parseRCA(rcaRaw)
		if t, ok := catalog.Get(a.ResourceType); ok {
			a.Provider = t.Provider
			if m, ok := t.Metric(a.MetricKey); ok {
				a.MetricLabel, a.Unit = m.Label, m.Unit
			}
		}
		return nil
	})
	return a, err
}

func isNoRowsErr(err error) bool { return err == pgx.ErrNoRows }

// rcaDoc is the shape stored in alerts.rca.
//
// A document rather than a plain string because an alarm accumulates more than one
// observation, and because the mute reason belongs with the rest of the human
// record instead of in its own column.
type rcaDoc struct {
	Notes      []model.RCANote `json:"notes,omitempty"`
	MuteReason string          `json:"mute_reason,omitempty"`
}

func parseRCA(raw []byte) ([]model.RCANote, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	var d rcaDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, ""
	}
	return d.Notes, d.MuteReason
}

func loadRCA(ctx context.Context, tx pgx.Tx, id string) (rcaDoc, error) {
	var raw []byte
	var d rcaDoc
	if err := tx.QueryRow(ctx, `SELECT rca FROM alerts WHERE id = $1::uuid`, id).
		Scan(&raw); err != nil {
		if isNoRowsErr(err) {
			return d, ErrNotFound
		}
		return d, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &d)
	}
	return d, nil
}

func (s *Store) MuteAlarm(ctx context.Context, id string, until time.Time, reason, userID string) (model.Alarm, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		doc, err := loadRCA(ctx, tx, id)
		if err != nil {
			return err
		}
		doc.MuteReason = reason
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE alerts SET muted_until = $2, rca = $3::jsonb, updated_at = now()
			 WHERE id = $1::uuid AND state IN ('open','acknowledged','suppressed')`,
			id, until, raw)
		if err != nil {
			return err
		}
		// Refusing on a closed alarm rather than silently doing nothing: muting
		// something already resolved means the operator is looking at a stale list.
		if tag.RowsAffected() == 0 {
			return invalid("that alarm is not open, so there is nothing to mute")
		}
		return nil
	})
	if err != nil {
		return model.Alarm{}, err
	}
	return s.alarmByID(ctx, id)
}

func (s *Store) UnmuteAlarm(ctx context.Context, id, userID string) (model.Alarm, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts SET muted_until = NULL, updated_at = now()
			 WHERE id = $1::uuid AND muted_until IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return invalid("that alarm is not muted")
		}
		return nil
	})
	if err != nil {
		return model.Alarm{}, err
	}
	return s.alarmByID(ctx, id)
}

func (s *Store) ResolveAlarm(ctx context.Context, id, reason, userID string) (model.Alarm, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		doc, err := loadRCA(ctx, tx, id)
		if err != nil {
			return err
		}
		// The reason goes into the same record as the root-cause notes, attributed
		// and timestamped. A manual close with no trace is the kind of thing that
		// looks like a bug six weeks later.
		doc.Notes = append(doc.Notes, model.RCANote{
			Note: "Closed by hand: " + reason, Author: userID, At: time.Now(),
		})
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE alerts
			   SET state = 'resolved', resolved_at = now(), rca = $2::jsonb,
			       updated_at = now()
			 WHERE id = $1::uuid AND state IN ('open','acknowledged','suppressed')`,
			id, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return invalid("that alarm is already closed")
		}
		// Close the matching outage too, so availability and the alarm agree. An
		// alarm resolved by hand while its outage stays open would keep counting
		// downtime for a problem somebody has declared over.
		if _, err := tx.Exec(ctx, `
			UPDATE outages o
			   SET ended_at = now(),
			       duration_sec = extract(epoch FROM (now() - o.started_at))::int,
			       root_cause = coalesce(o.root_cause, $2)
			 FROM alerts a
			WHERE a.id = $1::uuid AND o.resource_id = a.resource_id
			  AND o.ended_at IS NULL`, id, "Closed by hand: "+reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.Alarm{}, err
	}
	return s.alarmByID(ctx, id)
}

func (s *Store) AnnotateAlarm(ctx context.Context, id, note, userID string) (model.Alarm, error) {
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		doc, err := loadRCA(ctx, tx, id)
		if err != nil {
			return err
		}
		doc.Notes = append(doc.Notes, model.RCANote{Note: note, Author: userID, At: time.Now()})
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		// Notes are allowed on closed alarms, deliberately. Most root causes are
		// worked out after the incident, not during it.
		tag, err := tx.Exec(ctx,
			`UPDATE alerts SET rca = $2::jsonb, updated_at = now() WHERE id = $1::uuid`,
			id, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return model.Alarm{}, err
	}
	return s.alarmByID(ctx, id)
}

func (s *Store) AlarmNotifications(ctx context.Context, id string) ([]store.AlertLogEntry, error) {
	out := []store.AlertLogEntry{}
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
			WHERE n.alert_id = $1::uuid
			ORDER BY n.id`, id)
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
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

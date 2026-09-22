package alert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/mail"
)

// Delivery.
//
// Reads the intents the evaluator queued and sends them. Kept separate from the
// decision so a send that fails is recorded against a specific intent and retried,
// rather than being lost between passes.
//
// Runs inside the alerter's own loop rather than as another service. It has no work
// of its own to schedule — it reacts to rows the same pass just wrote — and a
// second unit would be a second thing to notice had stopped.

// maxAttempts caps retries. Three is enough to ride out a relay hiccup; beyond
// that the failure is configuration, and retrying forever would turn one broken
// channel into a permanent write load with no one looking at the error.
const maxAttempts = 3

// sendBatch is how many messages one pass will send.
//
// Deliberately small. The alerter ticks every minute, so this is a rate limit in
// disguise: if something has gone very wrong and two hundred alerts open at once,
// the mailbox fills at twenty a minute instead of two hundred at once, and there is
// time to mute or fix before the relay starts refusing.
const sendBatch = 20

// Sender delivers queued notifications.
type Sender struct {
	mailer  *mail.Sender
	baseURL string
}

// NewSender returns a sender, or nil when no relay is configured.
func NewSender(m *mail.Sender, baseURL string) *Sender {
	if m == nil {
		return nil
	}
	return &Sender{mailer: m, baseURL: strings.TrimRight(baseURL, "/")}
}

// pending is one claimed delivery.
type pending struct {
	id          int64
	alertID     string
	level       int
	channelType string
	channelName string
	recipient   string
	secretRef   string

	severity   string
	metricKey  string
	observed   *float64
	threshold  *float64
	message    string
	openedAt   time.Time
	resolvedAt *time.Time
	resourceID string
	// typeName is the catalog's display name, not the type code. An email that
	// says OCI_AUTONOMOUS_DB instead of Autonomous Database reads like a database
	// dump, which is what the first version of this did.
	typeName    string
	displayName string
	region      string
	// metricLabel and unit also come from the catalog, so the message can say
	// "Storage Utilisation is 97%" rather than "storage_utilization is 97".
	metricLabel string
	unit        string
}

// recovery reports whether this is a "it came back" message.
func (p pending) recovery() bool { return p.level < 0 }

// Deliver sends up to one batch of queued notifications.
//
// Returns how many were sent and how many failed. Errors from individual sends are
// recorded on the row and do not fail the pass: one unreachable channel must not
// stop the others.
func (e *Evaluator) Deliver(ctx context.Context, s *Sender) (sent, failed int, err error) {
	if s == nil {
		// No relay. The rows stay pending and the Alert Logs page shows them, which
		// is the honest state: the intent exists and cannot be carried out.
		return 0, 0, nil
	}

	var batch []pending
	err = e.withTx(ctx, func(tx pgx.Tx) error {
		// Claimed by setting attempts, so two passes cannot send the same row. The
		// row is the lock; SKIP LOCKED keeps a slow send from blocking the next
		// pass entirely.
		rows, err := tx.Query(ctx, `
			WITH claimed AS (
			  SELECT n.id FROM alert_notifications n
			   WHERE (n.state = 'pending'
			          OR (n.state = 'failed' AND n.attempts < $1
			              AND n.created_at > now() - interval '6 hours'))
			   ORDER BY n.id
			   LIMIT $2
			   FOR UPDATE SKIP LOCKED
			)
			UPDATE alert_notifications n
			   SET attempts = n.attempts + 1
			 WHERE n.id IN (SELECT id FROM claimed)
			RETURNING n.id, n.alert_id::text, n.level,
			          coalesce(n.recipient,''), coalesce(n.channel_id::text,'')`,
			maxAttempts, sendBatch)
		if err != nil {
			return err
		}
		type claim struct {
			id                   int64
			alertID              string
			level                int
			recipient, channelID string
		}
		var claims []claim
		for rows.Next() {
			var c claim
			if err := rows.Scan(&c.id, &c.alertID, &c.level, &c.recipient, &c.channelID); err != nil {
				rows.Close()
				return err
			}
			claims = append(claims, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, c := range claims {
			p := pending{id: c.id, alertID: c.alertID, level: c.level, recipient: c.recipient}
			if c.channelID != "" {
				if err := tx.QueryRow(ctx, `
					SELECT c.channel_type, c.display_name, coalesce(c.secret_ref,''),
					       coalesce(c.config->>'address','')
					FROM notification_channels c WHERE c.id = $1::uuid`, c.channelID).
					Scan(&p.channelType, &p.channelName, &p.secretRef, &p.recipient); err != nil {
					if !errors.Is(err, pgx.ErrNoRows) {
						return err
					}
				}
			}
			var typeCode string
			if err := tx.QueryRow(ctx, `
				SELECT a.severity, coalesce(a.metric_key,''), a.observed_value,
				       a.threshold_value, coalesce(a.message,''), a.opened_at, a.resolved_at,
				       r.id::text, r.display_name, r.resource_type, coalesce(r.region,'')
				FROM alerts a JOIN resources r ON r.id = a.resource_id
				WHERE a.id = $1::uuid`, c.alertID).
				Scan(&p.severity, &p.metricKey, &p.observed, &p.threshold, &p.message,
					&p.openedAt, &p.resolvedAt, &p.resourceID, &p.displayName,
					&typeCode, &p.region); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			// The catalog is the authority on how a type and a metric are named, so
			// the email spells them the same way every screen does.
			p.typeName = typeCode
			if t, ok := catalog.Get(typeCode); ok {
				p.typeName = t.DisplayName
				if m, ok := t.Metric(p.metricKey); ok {
					p.metricLabel, p.unit = m.Label, m.Unit
				}
			}
			batch = append(batch, p)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	for _, p := range batch {
		sendErr := s.send(p)
		state, msg := "sent", ""
		if sendErr != nil {
			state, msg = "failed", sendErr.Error()
			failed++
		} else {
			sent++
		}
		// Recorded in its own transaction so one failure cannot roll back the
		// record of the sends that worked.
		if werr := e.withTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE alert_notifications
				   SET state = $2, last_error = nullif($3,''),
				       sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
				 WHERE id = $1`, p.id, state, msg)
			return err
		}); werr != nil {
			return sent, failed, werr
		}
	}
	return sent, failed, nil
}

// send delivers one message.
func (s *Sender) send(p pending) error {
	switch p.channelType {
	case "email", "":
		if p.recipient == "" {
			return errors.New("channel has no address")
		}
		return s.mailer.Send(alertEmail(p, s.baseURL))
	default:
		// Named plainly rather than silently dropped. A channel type nobody has
		// implemented is a configuration mistake, and the log should say which.
		return fmt.Errorf("%s delivery is not implemented; only email is", p.channelType)
	}
}

// severityWord is the human phrasing, since "down" alone reads oddly in a subject.
func severityWord(sev string) string {
	switch sev {
	case "down":
		return "DOWN"
	case "critical":
		return "CRITICAL"
	case "trouble":
		return "TROUBLE"
	case "up":
		return "RECOVERED"
	default:
		return strings.ToUpper(sev)
	}
}

// unitSuffix renders a value with its unit attached the way a person writes it.
//
// Without this the message said "storage_utilization is 97", which forces the
// reader to guess whether that is a percentage, a gigabyte count or a number of
// sessions — and 97 means something very different in each.
func unitSuffix(v float64, unit string) string {
	n := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	switch unit {
	case "percent":
		return n + "%"
	case "milliseconds":
		return n + " ms"
	case "seconds":
		return n + " s"
	case "bytes":
		return humanBytes(v)
	case "bytes_per_sec":
		return humanBytes(v) + "/s"
	case "count", "":
		return n
	default:
		return n + " " + unit
	}
}

// headline is the one line that has to survive being read on a lock screen.
//
// Leads with the measurement rather than the metric name, because the number is
// what tells somebody whether to get out of bed.
func headline(p pending) string {
	label := p.metricLabel
	if label == "" {
		label = strings.ReplaceAll(p.metricKey, "_", " ")
	}
	switch {
	case p.observed != nil && p.threshold != nil:
		return fmt.Sprintf("%s is %s (threshold %s)",
			label, unitSuffix(*p.observed, p.unit), unitSuffix(*p.threshold, p.unit))
	case p.observed != nil:
		return fmt.Sprintf("%s is %s", label, unitSuffix(*p.observed, p.unit))
	case p.message != "":
		return p.message
	case p.severity == "down":
		return "The monitor is not responding"
	default:
		return label
	}
}

// shortHeadline is the subject-line form: label and value only.
//
// The subject has to survive being truncated on a lock screen at roughly forty
// characters, and the threshold is the part a reader can do without — knowing
// storage is at 97% is actionable whether or not the limit is stated.
func shortHeadline(p pending) string {
	label := p.metricLabel
	if label == "" {
		label = strings.ReplaceAll(p.metricKey, "_", " ")
	}
	if p.observed != nil {
		return fmt.Sprintf("%s %s", label, unitSuffix(*p.observed, p.unit))
	}
	if p.severity == "down" {
		return "not responding"
	}
	if label != "" {
		return label
	}
	return p.message
}

// bigValue is the figure shown large in the HTML, or empty when there is no single
// number to show — a down check has no measurement, only an absence.
func bigValue(p pending) string {
	if p.observed == nil {
		return ""
	}
	return unitSuffix(*p.observed, p.unit)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Minute:
		return "1 minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		return "1 hour"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func severityColour(sev string) string {
	switch sev {
	case "down":
		return "#b3261e"
	case "critical":
		return "#c2410c"
	case "trouble":
		return "#a16207"
	default:
		return "#2e8b46"
	}
}

func orDash(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

// alertEmail composes the message.
//
// The subject is the monitor, the state and the measurement, in that order, because
// on a phone that is all anybody reads. The body repeats the number large, gives the
// three facts needed to decide whether it matters — how long, where, which
// escalation — and then a link. No greeting and no preamble: an alert email is read
// in two seconds or not at all.
//
// The footer says how to stop the emails. Someone who cannot find that will build a
// mail filter instead, and a filtered alert is worse than no alert because everyone
// still believes it is being watched.
func alertEmail(p pending, baseURL string) mail.Message {
	link := fmt.Sprintf("%s/monitor/%s", baseURL, p.resourceID)

	if p.recovery() {
		dur := "an unknown time"
		if p.resolvedAt != nil {
			dur = humanDuration(p.resolvedAt.Sub(p.openedAt))
		}
		text := fmt.Sprintf(`%s is back to normal.

  Monitor    %s (%s)
  Was        %s
  Reason     %s
  Lasted     %s
  Started    %s

  %s

-- NimbusEye
`, p.displayName, p.displayName, p.typeName, severityWord(p.severity), headline(p),
			dur, p.openedAt.UTC().Format("2 Jan 2006, 15:04 UTC"), link)
		return mail.Message{
			To:      []string{p.recipient},
			Subject: fmt.Sprintf("RECOVERED  %s", p.displayName),
			Text:    text,
			HTML:    emailHTML(p, headline(p), "", dur, link, true),
		}
	}

	head := headline(p)
	subject := fmt.Sprintf("%s  %s - %s",
		severityWord(p.severity), p.displayName, shortHeadline(p))
	if p.level > 0 {
		// "still open" rather than "escalation 2": the reader needs to know this is
		// a repeat, and the precise stage is in the body where there is room.
		subject += " (still open)"
	}

	esc := "first notification"
	if p.level > 0 {
		esc = fmt.Sprintf("escalation %d, open without being acknowledged", p.level)
	}

	text := fmt.Sprintf(`%s

  Monitor    %s (%s)
  Severity   %s
  Ongoing    %s (since %s)
  Region     %s
  Stage      %s

  %s

To stop these messages: acknowledge or mute the alarm on the Alarms page, or
schedule maintenance if the work is planned.

-- NimbusEye
`, head, p.displayName, p.typeName, severityWord(p.severity),
		humanDuration(time.Since(p.openedAt)),
		p.openedAt.UTC().Format("2 Jan 2006, 15:04 UTC"),
		orDash(p.region), esc, link)

	return mail.Message{
		To: []string{p.recipient}, Subject: subject, Text: text,
		HTML: emailHTML(p, head, esc, humanDuration(time.Since(p.openedAt)), link, false),
	}
}

// emailHTML renders both the alert and the recovery layouts, which differ only in
// wording and colour. One function because two near-identical templates drift.
func emailHTML(p pending, head, stage, dur, link string, recovered bool) string {
	colour := severityColour(p.severity)
	band := severityWord(p.severity)
	if recovered {
		colour, band = "#2e8b46", "RECOVERED"
	}

	// With a value block the headline would repeat it in words, so the sentence is
	// dropped and the number carries it. Without one — a down check has nothing to
	// measure — the sentence is all there is.
	big := bigValue(p)
	subhead := ""
	if big == "" || recovered {
		subhead = fmt.Sprintf(
			`<div style="font-size:14px;color:#475569;margin-top:5px">%s</div>`, head)
	}
	valueBlock := ""
	if big != "" && !recovered {
		thr := ""
		if p.threshold != nil {
			thr = fmt.Sprintf(
				`<div style="font-size:12px;color:#94a3b8;margin-top:2px">threshold %s</div>`,
				unitSuffix(*p.threshold, p.unit))
		}
		label := p.metricLabel
		if label == "" {
			label = strings.ReplaceAll(p.metricKey, "_", " ")
		}
		valueBlock = fmt.Sprintf(`
      <div style="margin:18px 0 0;padding:14px 16px;background:#f8fafc;border:1px solid #e2e8f0;border-radius:6px">
        <div style="font-size:12px;color:#64748b;text-transform:uppercase;letter-spacing:.04em">%s</div>
        <div style="font-size:30px;line-height:1.1;font-weight:700;color:%s;margin-top:4px">%s</div>
        %s
      </div>`, label, colour, big, thr)
	}

	rows := fmt.Sprintf(`
        <tr><td style="padding:3px 16px 3px 0;color:#94a3b8">Monitor</td><td style="padding:3px 0"><strong>%s</strong></td></tr>
        <tr><td style="padding:3px 16px 3px 0;color:#94a3b8">Type</td><td style="padding:3px 0">%s</td></tr>
        <tr><td style="padding:3px 16px 3px 0;color:#94a3b8">Region</td><td style="padding:3px 0">%s</td></tr>
        <tr><td style="padding:3px 16px 3px 0;color:#94a3b8">%s</td><td style="padding:3px 0">%s</td></tr>`,
		p.displayName, p.typeName, orDash(p.region),
		map[bool]string{true: "Lasted", false: "Ongoing"}[recovered], dur)
	if stage != "" {
		rows += fmt.Sprintf(
			`<tr><td style="padding:3px 16px 3px 0;color:#94a3b8">Stage</td><td style="padding:3px 0">%s</td></tr>`,
			stage)
	}

	footer := `To stop these messages, acknowledge or mute the alarm, or schedule
        maintenance if the work is planned.`
	if recovered {
		footer = `No action needed. This is the closing message for that alarm.`
	}

	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:24px 16px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1e293b">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:10px;overflow:hidden">
    <div style="background:%s;padding:11px 20px;color:#ffffff;font-size:12px;font-weight:700;letter-spacing:.08em">%s</div>
    <div style="padding:20px">
      <div style="font-size:19px;font-weight:650;line-height:1.3">%s</div>
      %s%s
      <table style="margin:18px 0 0;font-size:13px;line-height:1.6;border-collapse:collapse">%s</table>
      <div style="margin:22px 0 0">
        <a href="%s" style="display:inline-block;background:#2e8b46;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:6px;font-size:14px;font-weight:600">Open in NimbusEye</a>
      </div>
      <p style="font-size:11px;line-height:1.6;color:#94a3b8;margin:20px 0 0;padding-top:14px;border-top:1px solid #f1f5f9">%s</p>
    </div>
  </div>
</body></html>`, colour, band, p.displayName, subhead, valueBlock, rows, link, footer)
}

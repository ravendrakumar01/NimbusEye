package alert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

	severity    string
	metricKey   string
	observed    *float64
	threshold   *float64
	message     string
	openedAt    time.Time
	resolvedAt  *time.Time
	resourceID  string
	displayName string
	typeName    string
	region      string
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
			if err := tx.QueryRow(ctx, `
				SELECT a.severity, coalesce(a.metric_key,''), a.observed_value,
				       a.threshold_value, coalesce(a.message,''), a.opened_at, a.resolved_at,
				       r.id::text, r.display_name, r.resource_type, coalesce(r.region,'')
				FROM alerts a JOIN resources r ON r.id = a.resource_id
				WHERE a.id = $1::uuid`, c.alertID).
				Scan(&p.severity, &p.metricKey, &p.observed, &p.threshold, &p.message,
					&p.openedAt, &p.resolvedAt, &p.resourceID, &p.displayName,
					&p.typeName, &p.region); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
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
		return "Trouble"
	case "up":
		return "Recovered"
	default:
		return sev
	}
}

func fmtVal(v *float64) string {
	if v == nil {
		return "—"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", *v), "0"), ".")
}

// alertEmail composes the message.
//
// The subject carries the monitor name and the state, because that is all anyone
// reads on a phone at 3am. The body leads with the number that tripped and how long
// it has been going, then the link. No preamble: an alert email is read in two
// seconds or not at all.
func alertEmail(p pending, baseURL string) mail.Message {
	link := fmt.Sprintf("%s/monitor/%s", baseURL, p.resourceID)

	if p.recovery() {
		dur := "an unknown time"
		if p.resolvedAt != nil {
			dur = humanDuration(p.resolvedAt.Sub(p.openedAt))
		}
		subject := fmt.Sprintf("Recovered: %s", p.displayName)
		text := fmt.Sprintf(`%s is back to normal.

Monitor:   %s (%s)
Was:       %s
Duration:  %s
Opened:    %s

%s

— NimbusEye
`, p.displayName, p.displayName, p.typeName, severityWord(p.severity), dur,
			p.openedAt.UTC().Format("2006-01-02 15:04 UTC"), link)
		return mail.Message{
			To: []string{p.recipient}, Subject: subject, Text: text,
			HTML: recoveryHTML(p, dur, link),
		}
	}

	escalation := ""
	if p.level > 0 {
		escalation = fmt.Sprintf(" (escalation %d)", p.level)
	}
	subject := fmt.Sprintf("%s: %s%s", severityWord(p.severity), p.displayName, escalation)

	detail := p.message
	if detail == "" && p.metricKey != "" {
		detail = fmt.Sprintf("%s is %s (threshold %s)",
			p.metricKey, fmtVal(p.observed), fmtVal(p.threshold))
	}

	text := fmt.Sprintf(`%s

Monitor:   %s (%s)
Severity:  %s
Since:     %s (%s ago)
Region:    %s

%s

— NimbusEye
`, detail, p.displayName, p.typeName, severityWord(p.severity),
		p.openedAt.UTC().Format("2006-01-02 15:04 UTC"),
		humanDuration(time.Since(p.openedAt)),
		orDash(p.region), link)

	return mail.Message{
		To: []string{p.recipient}, Subject: subject, Text: text,
		HTML: alertHTML(p, detail, link),
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func severityColour(sev string) string {
	switch sev {
	case "down":
		return "#c2382c"
	case "critical":
		return "#d9822b"
	case "trouble":
		return "#c9a227"
	default:
		return "#2e8b46"
	}
}

func alertHTML(p pending, detail, link string) string {
	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1e293b">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:8px;overflow:hidden">
    <div style="background:%s;padding:12px 20px;color:#ffffff;font-size:14px;font-weight:700;letter-spacing:.02em">%s</div>
    <div style="padding:20px">
      <p style="margin:0;font-size:16px;font-weight:600">%s</p>
      <p style="margin:8px 0 0;font-size:14px;line-height:1.6">%s</p>
      <table style="margin:16px 0 0;font-size:13px;line-height:1.7;color:#475569">
        <tr><td style="padding-right:14px;color:#94a3b8">Type</td><td>%s</td></tr>
        <tr><td style="padding-right:14px;color:#94a3b8">Region</td><td>%s</td></tr>
        <tr><td style="padding-right:14px;color:#94a3b8">Since</td><td>%s (%s ago)</td></tr>
      </table>
      <p style="margin:20px 0 0">
        <a href="%s" style="display:inline-block;background:#2e8b46;color:#ffffff;text-decoration:none;padding:9px 16px;border-radius:6px;font-size:14px;font-weight:600">Open in NimbusEye</a>
      </p>
    </div>
  </div>
</body></html>`,
		severityColour(p.severity), severityWord(p.severity), p.displayName, detail,
		p.typeName, orDash(p.region),
		p.openedAt.UTC().Format("2006-01-02 15:04 UTC"),
		humanDuration(time.Since(p.openedAt)), link)
}

func recoveryHTML(p pending, dur, link string) string {
	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1e293b">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:8px;overflow:hidden">
    <div style="background:#2e8b46;padding:12px 20px;color:#ffffff;font-size:14px;font-weight:700">RECOVERED</div>
    <div style="padding:20px">
      <p style="margin:0;font-size:16px;font-weight:600">%s is back to normal</p>
      <table style="margin:14px 0 0;font-size:13px;line-height:1.7;color:#475569">
        <tr><td style="padding-right:14px;color:#94a3b8">Was</td><td>%s</td></tr>
        <tr><td style="padding-right:14px;color:#94a3b8">Duration</td><td>%s</td></tr>
      </table>
      <p style="margin:20px 0 0">
        <a href="%s" style="display:inline-block;background:#2e8b46;color:#ffffff;text-decoration:none;padding:9px 16px;border-radius:6px;font-size:14px;font-weight:600">Open in NimbusEye</a>
      </p>
    </div>
  </div>
</body></html>`, p.displayName, severityWord(p.severity), dur, link)
}

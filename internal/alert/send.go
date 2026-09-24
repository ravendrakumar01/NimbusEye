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

	// Context pulled from the resource's attributes. These are what turn "a number
	// is past a line" into something somebody can act on without opening a console.
	compartment string
	// addresses identifies a resource whose display name is a UUID, which is most
	// load balancers in most tenancies.
	addresses []string
	// unhealthyBackends names the specific backends that are failing. The metric
	// says how many; only this says which.
	unhealthyBackends []string
	listeners         []string
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
			// Enrichment the collector stored against the resource. Read here rather
			// than joined in the query above because it is a JSON document, and
			// pulling fields out in SQL would put the shape in two places.
			var attrs map[string]any
			if err := tx.QueryRow(ctx,
				`SELECT attributes FROM resources WHERE id = $1::uuid`, p.resourceID).
				Scan(&attrs); err == nil {
				if v, ok := attrs["compartment_name"].(string); ok {
					p.compartment = v
				}
				p.addresses = stringsFrom(attrs["addresses"])
				p.unhealthyBackends = stringsFrom(attrs["unhealthy_backends"])
				p.listeners = stringsFrom(attrs["listeners"])
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

// stringsFrom pulls a string list out of a JSON attribute, tolerating both a real
// array and a single value, because provider payloads are not consistent about it.
func stringsFrom(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
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

// subjectName is the monitor as it should appear in a subject line.
//
// Cloud resources are not always named by people. This tenancy has load balancers
// called a087361c-fc7d-11e9-ace0-0a580aed6749, which ate the whole subject and left
// no room for the measurement — the one thing the subject exists to carry. A bare
// identifier is shortened and prefixed with its type, so "Load Balancer a087361c"
// says more in a quarter of the space than the full UUID said.
func subjectName(p pending) string {
	name := p.displayName
	if looksLikeID(name) {
		short := name
		if i := strings.IndexByte(short, '-'); i > 0 {
			short = short[:i]
		}
		if p.typeName != "" {
			return p.typeName + " " + short
		}
		return short
	}
	// Anything very long still gets clipped, because a subject truncated by the
	// client cuts from the end, which is where the measurement is.
	const maxName = 38
	if len(name) > maxName {
		return name[:maxName-3] + "..."
	}
	return name
}

// looksLikeID reports whether a name is an identifier rather than something a
// person chose: hex and dashes only, and long enough that nobody typed it.
func looksLikeID(s string) bool {
	if len(s) < 20 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'f',
			r >= 'A' && r <= 'F',
			r == '-':
		default:
			return false
		}
	}
	return true
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
			Subject: fmt.Sprintf("RECOVERED  %s", subjectName(p)),
			Text:    text,
			HTML:    emailHTML(p, headline(p), "", dur, link, true),
		}
	}

	head := headline(p)
	subject := fmt.Sprintf("%s  %s - %s",
		severityWord(p.severity), subjectName(p), shortHeadline(p))
	if p.level > 0 {
		// "still open" rather than "escalation 2": the reader needs to know this is
		// a repeat, and the precise stage is in the body where there is room.
		subject += " (still open)"
	}

	esc := "first notification"
	if p.level > 0 {
		esc = fmt.Sprintf("escalation %d, open without being acknowledged", p.level)
	}

	// Facts as aligned label/value pairs, built rather than templated so an optional
	// row does not leave a gap and the labels stay in one column.
	var facts strings.Builder
	fact := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&facts, "  %-12s %s\n", k, v)
		}
	}
	fact("Monitor", fmt.Sprintf("%s (%s)", p.displayName, p.typeName))
	fact("Severity", severityWord(p.severity))
	if len(p.addresses) > 0 {
		fact("Address", strings.Join(p.addresses, ", "))
	}
	fact("Compartment", p.compartment)
	fact("Region", orDash(p.region))
	if len(p.listeners) > 0 {
		fact("Listeners", strings.Join(p.listeners, ", "))
	}
	fact("Ongoing", fmt.Sprintf("%s (since %s)",
		humanDuration(time.Since(p.openedAt)),
		p.openedAt.UTC().Format("2 Jan 2006, 15:04 UTC")))
	fact("Stage", esc)

	// The specific backends, after the facts rather than inside them: it is a list,
	// and threading a list through a label column reads badly.
	backends := ""
	if len(p.unhealthyBackends) > 0 {
		var b strings.Builder
		b.WriteString("\nFailing backends:\n")
		for _, x := range p.unhealthyBackends {
			fmt.Fprintf(&b, "  %s\n", x)
		}
		backends = b.String()
	}

	guidance := hintFor(p.metricKey)
	if guidance == "" && p.severity == "down" {
		guidance = downHint
	}
	checkBlock := ""
	if guidance != "" {
		checkBlock = "\nWhat to check:\n  " + wrapText(guidance, 74, "  ") + "\n"
	}

	text := fmt.Sprintf(`%s

%s%s%s
  %s

To stop these messages: acknowledge or mute the alarm on the Alarms page, or
schedule maintenance if the work is planned.

-- NimbusEye
`, head, facts.String(), backends, checkBlock, link)

	return mail.Message{
		To: []string{p.recipient}, Subject: subject, Text: text,
		HTML: emailHTML(p, head, esc, humanDuration(time.Since(p.openedAt)), link, false),
	}
}

// emailHTML renders both the alert and the recovery layouts.
//
// Written as nested tables with fixed widths, which looks like 1999 and is what
// actually works. Outlook renders HTML through Word's engine: it ignores max-width
// and margin:auto on a div, so the first version of this spread edge to edge in the
// reading pane with its rounded card gone and the button flattened into green text.
// Screenshots from the recipient's Outlook are the only reason we know — it looked
// correct in every browser.
//
// The rules being followed, none of them optional for Outlook:
//
//	Layout in tables with explicit width attributes, not CSS on divs.
//	Centring via align="center" on the outer cell, not margin:auto.
//	The button is a table cell with a background colour, not a padded anchor.
//	Inline styles only; Outlook drops most of a <style> block.
//	No border-radius, no flexbox, no shorthand background.
func emailHTML(p pending, head, stage, dur, link string, recovered bool) string {
	colour := severityColour(p.severity)
	band := severityWord(p.severity)
	if recovered {
		colour, band = "#2e8b46", "RECOVERED"
	}

	// With a value block the headline would repeat it in words, so the sentence is
	// dropped and the number carries it. Without one - a down check has nothing to
	// measure - the sentence is all there is.
	big := bigValue(p)
	subhead := ""
	if big == "" || recovered {
		subhead = fmt.Sprintf(
			`<tr><td style="padding:2px 24px 0;font-family:Arial,Helvetica,sans-serif;`+
				`font-size:14px;color:#475569">%s</td></tr>`, head)
	}

	valueBlock := ""
	if big != "" && !recovered {
		label := p.metricLabel
		if label == "" {
			label = strings.ReplaceAll(p.metricKey, "_", " ")
		}
		thr := ""
		if p.threshold != nil {
			thr = fmt.Sprintf(
				`<tr><td style="padding:2px 0 0;font-family:Arial,Helvetica,sans-serif;`+
					`font-size:12px;color:#94a3b8">threshold %s</td></tr>`,
				unitSuffix(*p.threshold, p.unit))
		}
		valueBlock = fmt.Sprintf(`
          <tr><td style="padding:16px 24px 0">
            <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"
                   style="background-color:#f8fafc;border:1px solid #e2e8f0">
              <tr><td style="padding:14px 16px">
                <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                  <tr><td style="font-family:Arial,Helvetica,sans-serif;font-size:11px;
                                 color:#64748b;letter-spacing:.06em">%s</td></tr>
                  <tr><td style="padding:3px 0 0;font-family:Arial,Helvetica,sans-serif;
                                 font-size:28px;font-weight:bold;color:%s">%s</td></tr>
                  %s
                </table>
              </td></tr>
            </table>
          </td></tr>`, strings.ToUpper(label), colour, big, thr)
	}

	// Facts. A two-column table rather than a definition list, because Outlook adds
	// its own spacing to anything it does not recognise.
	factRow := func(k, v string) string {
		return fmt.Sprintf(
			`<tr>`+
				`<td width="86" valign="top" style="padding:4px 10px 4px 0;font-family:Arial,Helvetica,sans-serif;font-size:13px;color:#94a3b8">%s</td>`+
				`<td valign="top" style="padding:4px 0;font-family:Arial,Helvetica,sans-serif;font-size:13px;color:#1e293b">%s</td>`+
				`</tr>`, k, v)
	}
	facts := factRow("Monitor", "<strong>"+p.displayName+"</strong>")
	facts += factRow("Type", p.typeName)
	if len(p.addresses) > 0 {
		// The address goes high up, because for a resource named by a UUID it is the
		// only line that says which one this is.
		facts += factRow("Address", strings.Join(p.addresses, ", "))
	}
	if p.compartment != "" {
		facts += factRow("Compartment", p.compartment)
	}
	facts += factRow("Region", orDash(p.region))
	if len(p.listeners) > 0 {
		facts += factRow("Listeners", strings.Join(p.listeners, ", "))
	}
	if recovered {
		facts += factRow("Lasted", dur)
	} else {
		facts += factRow("Ongoing", dur)
	}
	if stage != "" {
		facts += factRow("Stage", stage)
	}

	// The specific backends that are failing, which is the whole difference between
	// "three are unhealthy" and something somebody can act on.
	backendBlock := ""
	if len(p.unhealthyBackends) > 0 && !recovered {
		items := ""
		for _, b := range p.unhealthyBackends {
			items += fmt.Sprintf(
				`<tr><td style="padding:2px 0;font-family:Arial,Helvetica,sans-serif;`+
					`font-size:13px;color:#b3261e">%s</td></tr>`, b)
		}
		backendBlock = fmt.Sprintf(`
          <tr><td style="padding:16px 24px 0">
            <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"
                   style="background-color:#fef2f2;border:1px solid #fecaca">
              <tr><td style="padding:12px 16px">
                <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                  <tr><td style="padding:0 0 4px;font-family:Arial,Helvetica,sans-serif;
                                 font-size:11px;color:#b3261e;letter-spacing:.06em">FAILING BACKENDS</td></tr>
                  %s
                </table>
              </td></tr>
            </table>
          </td></tr>`, items)
	}

	// What to look at first. Says where to look, not what the answer is: a hint that
	// guessed at the cause would be wrong often enough to be worse than silence.
	guidance := hintFor(p.metricKey)
	if guidance == "" && p.severity == "down" {
		guidance = downHint
	}
	checkBlock := ""
	if guidance != "" && !recovered {
		checkBlock = fmt.Sprintf(`
          <tr><td style="padding:16px 24px 0">
            <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"
                   style="background-color:#f8fafc;border-left:3px solid #2e8b46">
              <tr><td style="padding:12px 14px">
                <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                  <tr><td style="padding:0 0 4px;font-family:Arial,Helvetica,sans-serif;
                                 font-size:11px;color:#64748b;letter-spacing:.06em">WHAT TO CHECK</td></tr>
                  <tr><td style="font-family:Arial,Helvetica,sans-serif;font-size:13px;
                                 line-height:19px;color:#334155">%s</td></tr>
                </table>
              </td></tr>
            </table>
          </td></tr>`, guidance)
	}

	footer := "To stop these messages, acknowledge or mute the alarm, or schedule " +
		"maintenance if the work is planned."
	if recovered {
		footer = "No action needed. This is the closing message for that alarm."
	}

	return fmt.Sprintf(`<!doctype html>
<html xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch>
</o:OfficeDocumentSettings></xml><![endif]-->
<title>%s</title>
</head>
<body style="margin:0;padding:0;background-color:#f1f5f9">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"
       style="background-color:#f1f5f9">
  <tr><td align="center" style="padding:20px 10px">

    <table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0"
           style="width:560px;max-width:560px;background-color:#ffffff;border:1px solid #e2e8f0">

      <tr><td style="background-color:%s;padding:10px 24px;font-family:Arial,Helvetica,sans-serif;
                     font-size:12px;font-weight:bold;color:#ffffff;letter-spacing:.1em">%s</td></tr>

      <tr><td style="padding:18px 24px 0;font-family:Arial,Helvetica,sans-serif;
                     font-size:18px;font-weight:bold;color:#1e293b;word-break:break-all">%s</td></tr>
      %s
      %s

      <tr><td style="padding:16px 24px 0">
        <table role="presentation" cellpadding="0" cellspacing="0" border="0">%s</table>
      </td></tr>
      %s
      %s

      <tr><td style="padding:20px 24px 0">
        <table role="presentation" cellpadding="0" cellspacing="0" border="0">
          <tr><td align="center" bgcolor="#2e8b46" style="background-color:#2e8b46">
            <a href="%s" style="display:block;padding:11px 20px;font-family:Arial,Helvetica,sans-serif;
                                font-size:14px;font-weight:bold;color:#ffffff;text-decoration:none">
              Open in NimbusEye</a>
          </td></tr>
        </table>
      </td></tr>

      <tr><td style="padding:18px 24px 20px">
        <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0">
          <tr><td style="border-top:1px solid #f1f5f9;padding:12px 0 0;
                         font-family:Arial,Helvetica,sans-serif;font-size:11px;
                         line-height:16px;color:#94a3b8">%s</td></tr>
        </table>
      </td></tr>

    </table>

  </td></tr>
</table>
</body></html>`, band+" "+p.displayName, colour, band, p.displayName,
		subhead, valueBlock, facts, backendBlock, checkBlock, link, footer)
}

// wrapText breaks a paragraph at a column, for the plain-text alternative.
//
// Needed because the guidance lines are sentences, and an unwrapped 300-character
// line is unreadable in a terminal mail client and gets hard-wrapped at an
// arbitrary point by everything else.
func wrapText(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	for i, w := range words {
		if col > 0 && col+1+len(w) > width {
			b.WriteString("\n" + indent)
			col = 0
		} else if i > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(w)
		col += len(w)
	}
	return b.String()
}

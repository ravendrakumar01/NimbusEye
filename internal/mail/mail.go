// Package mail sends transactional email over SMTP.
//
// Small on purpose. NimbusEye sends two kinds of message — a password reset link
// and an alert notification — and both are short, plain, and must arrive. A
// templating engine and an HTML email framework would add far more surface than
// that needs.
//
// The password is read from a file at send time and never held in the config
// struct's exported fields longer than necessary, so it does not appear in logs
// that dump configuration.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Config describes the SMTP relay.
type Config struct {
	Host string
	Port int
	// User may be empty for a relay that does not authenticate.
	User string
	// PasswordFile is a path, not the password. Read at send time.
	PasswordFile string
	From         string
	FromName     string
	// STARTTLS upgrades a plaintext connection, which is what port 587 expects.
	// When false and the port is 465, implicit TLS is used instead.
	STARTTLS bool
	Timeout  time.Duration
}

// Sender delivers messages.
type Sender struct {
	cfg Config
}

// ErrNotConfigured means no relay is set up, so callers can degrade rather than
// fail. An unconfigured mailer is a normal state, not an error condition.
var ErrNotConfigured = errors.New("mail: no SMTP relay configured")

// New builds a Sender. It validates the configuration but does not connect:
// failing startup because a mail relay is briefly unreachable would take the whole
// API down for something that only matters when a message is actually sent.
func New(cfg Config) (*Sender, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, ErrNotConfigured
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, errors.New("mail: From address is required")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return nil, fmt.Errorf("mail: From address %q is not valid: %w", cfg.From, err)
	}
	if cfg.User != "" && cfg.PasswordFile == "" {
		return nil, errors.New("mail: a user was given without a password file")
	}
	return &Sender{cfg: cfg}, nil
}

// From reports the configured sender address, for display.
func (s *Sender) From() string { return s.cfg.From }

// Message is one email.
type Message struct {
	To      []string
	Subject string
	// Text is required. HTML is optional; when present the message is sent as
	// multipart/alternative so a plain-text client still gets something readable.
	Text string
	HTML string
}

// Send delivers a message.
func (s *Sender) Send(m Message) error {
	if len(m.To) == 0 {
		return errors.New("mail: no recipients")
	}
	if strings.TrimSpace(m.Text) == "" {
		// Refused rather than sent: an HTML-only message is unreadable to a
		// text client and looks like spam to several filters.
		return errors.New("mail: a plain-text body is required")
	}

	var auth smtp.Auth
	if s.cfg.User != "" {
		pw, err := s.password()
		if err != nil {
			return err
		}
		auth = smtp.PlainAuth("", s.cfg.User, pw, s.cfg.Host)
	}

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port))
	body := s.build(m)

	conn, err := net.DialTimeout("tcp", addr, s.cfg.Timeout)
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	// A relay that accepts the connection then stalls would otherwise hang the
	// request that triggered the send.
	_ = conn.SetDeadline(time.Now().Add(s.cfg.Timeout))

	if !s.cfg.STARTTLS && s.cfg.Port == 465 {
		// Implicit TLS: the connection is encrypted from the first byte.
		conn = tls.Client(conn, &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12})
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if s.cfg.STARTTLS {
		ok, _ := c.Extension("STARTTLS")
		if !ok {
			// Refusing rather than falling back to plaintext: credentials and a
			// password-reset link would otherwise cross the network in the clear.
			return errors.New("mail: relay does not offer STARTTLS but it was required")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mail: authentication rejected: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: sender rejected: %w", err)
	}
	for _, to := range m.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("mail: recipient %s rejected: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close body: %w", err)
	}
	return c.Quit()
}

// password reads the secret, refusing a world-readable file for the same reason
// the cloud credentials are checked.
func (s *Sender) password() (string, error) {
	fi, err := os.Stat(s.cfg.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("mail: cannot read password file: %w", err)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("mail: %s is world-accessible (mode %o); run chmod 640",
			s.cfg.PasswordFile, fi.Mode().Perm())
	}
	b, err := os.ReadFile(s.cfg.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("mail: cannot read password file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// build assembles the RFC 5322 message.
// quotedPrintable encodes a body for transport.
//
// Necessary rather than decorative. Without a Content-Transfer-Encoding the message
// declares itself 7-bit, and any byte above 127 then depends on the relay guessing
// right — which is how an em dash arrives as three mojibake characters. Resource
// names come from the customer's cloud and can contain anything, so this cannot be
// avoided by being careful with our own wording.
//
// Also enforces the 998-octet line limit that SMTP requires and that long HTML
// attributes break without warning.
func quotedPrintable(s string) string {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		// Writing to a strings.Builder cannot fail; fall back to the raw body
		// rather than sending nothing.
		return s
	}
	if err := w.Close(); err != nil {
		return s
	}
	return b.String()
}

func (s *Sender) build(m Message) []byte {
	from := s.cfg.From
	if s.cfg.FromName != "" {
		// Encoded, so a non-ASCII display name does not corrupt the header.
		from = mime.QEncoding.Encode("utf-8", s.cfg.FromName) + " <" + s.cfg.From + ">"
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(m.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	// Marks the message as transactional, so mailbox providers are less likely to
	// treat a password reset as bulk mail.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("X-Auto-Response-Suppress: All\r\n")

	if m.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		b.WriteString(quotedPrintable(normaliseNewlines(m.Text)))
		return []byte(b.String())
	}

	boundary := "nimbuseye-" + fmt.Sprint(time.Now().UnixNano())
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.WriteString(quotedPrintable(normaliseNewlines(m.Text)) + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.WriteString(quotedPrintable(normaliseNewlines(m.HTML)) + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// normaliseNewlines converts bare newlines to CRLF, which SMTP requires.
func normaliseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

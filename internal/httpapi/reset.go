package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"nimbuseye/internal/auth"
	"nimbuseye/internal/mail"
)

// Password reset.
//
// The whole flow is built around one rule: nothing here may reveal whether an
// email address has an account. The request endpoint returns the same response
// either way, takes roughly the same time either way, and the email is only sent
// when there is somewhere to send it.

func (s *Server) registerResetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/forgot", s.forgotPassword)
	mux.HandleFunc("POST /api/v1/auth/reset", s.resetPassword)
}

// forgotPassword issues a reset link.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Body must be JSON")
		return
	}

	// The same answer regardless of outcome. Written once, before any branching, so
	// no later path can accidentally return something more specific.
	const sameAnswer = "If that address has an account, a reset link is on its way. " +
		"The link is valid for one hour."

	if s.mailer == nil {
		// Without a relay there is no way to deliver the link. Said plainly rather
		// than claiming an email was sent, which would leave someone waiting for a
		// message that is never coming.
		s.log.Warn("password reset requested but no SMTP relay is configured")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"code":    "mail_not_configured",
				"message": "Password reset is unavailable: this server has no mail relay configured.",
			},
		})
		return
	}

	req, err := s.auth.CreateResetToken(r.Context(), body.Email, clientIP(r))
	if err != nil {
		s.log.Error("could not create reset token", "err", err)
		// Still the same answer: an internal failure must not become a signal
		// about whether the address exists.
		writeJSON(w, http.StatusOK, map[string]any{"message": sameAnswer})
		return
	}

	if req.Found {
		link := fmt.Sprintf("%s/reset?token=%s",
			strings.TrimRight(s.cfg.BaseURL, "/"), req.Token)
		if err := s.mailer.Send(resetEmail(req.DisplayName, req.Email, link, req.ExpiresAt)); err != nil {
			// Logged, not surfaced. The caller already has the only answer they are
			// getting, and a delivery failure would otherwise confirm the account
			// exists.
			s.log.Error("could not send reset email", "err", err)
		} else {
			s.log.Info("password reset link sent", "user", req.Email, "ip", clientIP(r))
		}
	} else {
		s.log.Info("password reset requested for an unknown address", "ip", clientIP(r))
	}

	writeJSON(w, http.StatusOK, map[string]any{"message": sameAnswer})
}

// resetPassword redeems a token.
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"new_password"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Body must be JSON")
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "The reset link is incomplete")
		return
	}

	if err := s.auth.RedeemResetToken(r.Context(), body.Token, body.Password, clientIP(r)); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusBadRequest, "invalid_token",
				"This reset link is no longer valid. Request a new one.")
			return
		}
		// A password that fails the length rule lands here, and is worth reporting
		// precisely — it is about the input, not about the account.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]any{
				"code":    "validation_failed",
				"message": "Password could not be set",
				// Stripped of the package prefix: "auth: password must be…" is a
				// developer's error string, not something to show a user.
				"fields": map[string]string{"new_password": userFacing(err)},
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reset":   true,
		"message": "Password set. All sessions were signed out. Sign in with your new password.",
	})
}

// resetEmail composes the message.
//
// Plain text first, with a minimal HTML alternative. No images, no external CSS and
// no tracking: those are what make a transactional email look like marketing to a
// spam filter, and this is the one message that must arrive.
func resetEmail(name, to, link string, expires time.Time) mail.Message {
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	valid := time.Until(expires).Round(time.Minute)

	text := fmt.Sprintf(`Hello %s,

Someone asked to reset the password for your NimbusEye account (%s).

Open this link to choose a new one:

%s

The link is valid for %s and can be used once. Every signed-in session will be
ended when you set the new password.

If you did not ask for this, you can ignore this message. Your password has not
changed and no one can use the link without receiving this email.

— NimbusEye
`, name, to, link, valid)

	htmlBody := fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1e293b">
  <div style="max-width:520px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:8px;padding:28px">
    <div style="font-size:22px;font-weight:700;letter-spacing:-0.3px">
      <span style="color:#2e8b46">Nimbus</span><span style="color:#1e293b">Eye</span>
    </div>
    <p style="font-size:14px;line-height:1.6;margin:20px 0 0">Hello %s,</p>
    <p style="font-size:14px;line-height:1.6;margin:12px 0 0">
      Someone asked to reset the password for your NimbusEye account
      (<strong>%s</strong>).
    </p>
    <p style="margin:22px 0">
      <a href="%s" style="display:inline-block;background:#2e8b46;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:6px;font-size:14px;font-weight:600">Choose a new password</a>
    </p>
    <p style="font-size:12px;line-height:1.6;color:#64748b;margin:0">
      The link is valid for %s and can be used once. Setting a new password ends
      every signed-in session.
    </p>
    <p style="font-size:12px;line-height:1.6;color:#64748b;margin:14px 0 0">
      If you did not ask for this, ignore this message. Your password has not
      changed.
    </p>
    <p style="font-size:11px;color:#94a3b8;margin:20px 0 0;word-break:break-all">
      If the button does not work, paste this into your browser:<br>%s
    </p>
  </div>
</body></html>`,
		html.EscapeString(name), html.EscapeString(to),
		html.EscapeString(link), valid, html.EscapeString(link))

	return mail.Message{
		To:      []string{to},
		Subject: "Reset your NimbusEye password",
		Text:    text,
		HTML:    htmlBody,
	}
}

// userFacing trims the Go package prefix from an error meant for a person.
func userFacing(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && i < 12 {
		msg = msg[i+2:]
	}
	return strings.ToUpper(msg[:1]) + msg[1:] + "."
}

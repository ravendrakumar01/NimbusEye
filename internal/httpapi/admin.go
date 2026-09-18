package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nimbuseye/internal/mail"
	"nimbuseye/internal/store"
	"nimbuseye/internal/store/pg"
)

// Administration endpoints.
//
// Everything here changes configuration that the alerter and collector read at
// runtime, so two things are enforced at the door rather than assumed:
//
//   Role. A viewer can read the configuration — being able to see which
//   thresholds apply is part of understanding an alert — but only an admin or
//   owner can change it. The check is on the server; hiding a button is a
//   convenience, not a control.
//
//   Audit. Every mutation is recorded with the acting user, the object and the
//   change. Configuration that silently drifts is how a monitoring tool ends up
//   not alerting without anyone knowing when that started.

// writable roles for administration.
var adminRoles = map[string]bool{"owner": true, "admin": true}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/threshold-profiles", s.listThresholdProfiles)
	mux.HandleFunc("GET /api/v1/admin/threshold-profiles/{id}", s.getThresholdProfile)
	mux.HandleFunc("PATCH /api/v1/admin/threshold-profiles/{id}", s.patchThresholdProfile)

	mux.HandleFunc("GET /api/v1/admin/notification-profiles", s.listNotificationProfiles)
	mux.HandleFunc("PATCH /api/v1/admin/notification-profiles/{id}", s.patchNotificationProfile)

	mux.HandleFunc("GET /api/v1/admin/channels", s.listChannels)
	mux.HandleFunc("POST /api/v1/admin/channels", s.createChannel)
	mux.HandleFunc("PATCH /api/v1/admin/channels/{id}", s.updateChannel)
	mux.HandleFunc("DELETE /api/v1/admin/channels/{id}", s.deleteChannel)

	mux.HandleFunc("GET /api/v1/admin/users", s.listUsers)
	mux.HandleFunc("POST /api/v1/admin/users", s.createUser)
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.updateUser)
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}", s.deleteUser)
	mux.HandleFunc("POST /api/v1/admin/users/{id}/unlock", s.unlockUser)

	mux.HandleFunc("GET /api/v1/admin/audit", s.listAudit)
}

// admin returns the administration interface, or explains why it is unavailable.
func (s *Server) admin(w http.ResponseWriter) (store.Administrator, bool) {
	a, ok := s.store.(store.Administrator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "admin_unavailable",
			"Administration needs the database backend. This instance is running the "+
				"in-memory store, where configuration changes would be discarded.")
		return nil, false
	}
	return a, true
}

// requireAdmin gates mutations and returns the acting user id for the audit trail.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no_session", "Sign in to continue.")
		return "", false
	}
	if !adminRoles[sess.User.Role] {
		// Naming the required role is deliberate: "forbidden" with no explanation
		// sends people to support, and the role is not sensitive information.
		writeError(w, http.StatusForbidden, "insufficient_role",
			"Changing this needs the admin or owner role. Yours is "+sess.User.Role+".")
		return "", false
	}
	return sess.User.ID, true
}

// adminError maps a store error onto a response, keeping validation messages
// visible and everything else generic.
func (s *Server) adminError(w http.ResponseWriter, what string, err error) {
	var inv pg.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "invalid", inv.Msg)
	case errors.Is(err, pg.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That no longer exists.")
	default:
		s.log.Error(what+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "The change could not be saved.")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	// Strict for user input: a misspelled field that is silently ignored is a
	// setting the operator believes they changed and did not.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "The request body could not be read: "+err.Error())
		return false
	}
	return true
}

/* -------------------------------------------------------------------------- */
/* Threshold profiles                                                          */
/* -------------------------------------------------------------------------- */

func (s *Server) listThresholdProfiles(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	ps, err := a.ThresholdProfiles(r.Context(), splitCSV(r.URL.Query().Get("provider")))
	if err != nil {
		s.adminError(w, "list threshold profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": ps, "count": len(ps)})
}

func (s *Server) getThresholdProfile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	p, err := a.ThresholdProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminError(w, "get threshold profile", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) patchThresholdProfile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.ThresholdProfileUpdate
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	p, err := a.UpdateThresholdProfile(r.Context(), id, in)
	if err != nil {
		s.adminError(w, "update threshold profile", err)
		return
	}
	s.audit(r, actor, "threshold_profile.update", "threshold_profile", id, map[string]any{
		"name": p.DisplayName, "rules": len(p.Rules),
		"down_polls_check": p.DownPollsCheck, "governs_resources": p.ResourceCount,
	})
	writeJSON(w, http.StatusOK, p)
}

/* -------------------------------------------------------------------------- */
/* Notification profiles                                                       */
/* -------------------------------------------------------------------------- */

func (s *Server) listNotificationProfiles(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	ps, err := a.NotificationProfiles(r.Context())
	if err != nil {
		s.adminError(w, "list notification profiles", err)
		return
	}
	// The delivery state is reported alongside, because a fully configured
	// routing policy still sends nothing if no channel exists. Leaving that to be
	// discovered during an incident is the failure this is meant to prevent.
	chans, _ := a.NotificationChannels(r.Context())
	enabled := 0
	for _, c := range chans {
		if c.Enabled {
			enabled++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles":         ps,
		"channels_total":   len(chans),
		"channels_enabled": enabled,
		"delivery_ready":   enabled > 0,
	})
}

func (s *Server) patchNotificationProfile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.NotificationProfileUpdate
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	p, err := a.UpdateNotificationProfile(r.Context(), id, in)
	if err != nil {
		s.adminError(w, "update notification profile", err)
		return
	}
	s.audit(r, actor, "notification_profile.update", "notification_profile", id, map[string]any{
		"name": p.DisplayName, "delay": p.NotificationDelay,
		"repeat_minutes": p.PersistentAlertInterval,
		"rules":          len(p.AlertRules), "escalations": len(p.EscalationLevels),
	})
	writeJSON(w, http.StatusOK, p)
}

/* -------------------------------------------------------------------------- */
/* Channels                                                                    */
/* -------------------------------------------------------------------------- */

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	cs, err := a.NotificationChannels(r.Context())
	if err != nil {
		s.adminError(w, "list channels", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": cs,
		// Whether the server can actually send email at all is a property of the
		// server, not of any channel, and it is the first thing to check when
		// nothing arrives.
		"smtp_configured": s.mailer != nil,
	})
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.ChannelInput
	if !decode(w, r, &in) {
		return
	}
	c, err := a.CreateChannel(r.Context(), in)
	if err != nil {
		s.adminError(w, "create channel", err)
		return
	}
	s.audit(r, actor, "channel.create", "notification_channel", c.ID, map[string]any{
		"name": c.DisplayName, "type": c.ChannelType,
	})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.ChannelInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	c, err := a.UpdateChannel(r.Context(), id, in)
	if err != nil {
		s.adminError(w, "update channel", err)
		return
	}
	s.audit(r, actor, "channel.update", "notification_channel", id, map[string]any{
		"name": c.DisplayName, "enabled": c.Enabled,
	})
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := a.DeleteChannel(r.Context(), id); err != nil {
		s.adminError(w, "delete channel", err)
		return
	}
	s.audit(r, actor, "channel.delete", "notification_channel", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

/* -------------------------------------------------------------------------- */
/* Users                                                                       */
/* -------------------------------------------------------------------------- */

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	us, err := a.AdminUsers(r.Context())
	if err != nil {
		s.adminError(w, "list users", err)
		return
	}
	me := ""
	if sess, ok := SessionFrom(r.Context()); ok {
		me = sess.User.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": us,
		// The UI needs to know which row is the caller so it can stop them
		// disabling or deleting themselves.
		"me":              me,
		"smtp_configured": s.mailer != nil,
	})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.UserInput
	if !decode(w, r, &in) {
		return
	}
	u, err := a.CreateUser(r.Context(), in)
	if err != nil {
		s.adminError(w, "create user", err)
		return
	}
	s.audit(r, actor, "user.invite", "user", u.ID, map[string]any{
		"email": u.Email, "role": u.Role,
	})

	// An invited user has no password. Sending the reset link is what makes the
	// invitation usable; when mail is unconfigured the response says so rather
	// than leaving an account nobody can reach.
	invited := false
	if s.mailer != nil && s.auth != nil {
		if err := s.sendInvite(r.Context(), u.Email); err != nil {
			s.log.Warn("invite email failed", "email", u.Email, "err", err)
		} else {
			invited = true
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":        u,
		"email_sent":  invited,
		"next_action": inviteHint(invited, s.mailer != nil),
	})
}

func inviteHint(sent, smtp bool) string {
	switch {
	case sent:
		return "A link to set a password has been emailed."
	case !smtp:
		return "No SMTP relay is configured, so no email was sent. The user must be sent a password reset link manually."
	default:
		return "The invitation email could not be sent. Ask the user to use Forgot password."
	}
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var in store.UserInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	// Self-demotion and self-disable are refused here rather than in the store,
	// because the store has no notion of who is asking.
	if id == actor {
		if in.Role != "" && !adminRoles[in.Role] {
			writeError(w, http.StatusBadRequest, "invalid",
				"You cannot remove your own admin access. Ask another owner to change it.")
			return
		}
		if in.Status != nil && *in.Status == "disabled" {
			writeError(w, http.StatusBadRequest, "invalid", "You cannot disable your own account.")
			return
		}
	}
	u, err := a.UpdateUser(r.Context(), id, in)
	if err != nil {
		s.adminError(w, "update user", err)
		return
	}
	s.audit(r, actor, "user.update", "user", id, map[string]any{
		"email": u.Email, "role": u.Role, "status": u.Status,
	})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == actor {
		writeError(w, http.StatusBadRequest, "invalid",
			"You cannot delete your own account. Ask another owner to do it.")
		return
	}
	if err := a.DeleteUser(r.Context(), id); err != nil {
		s.adminError(w, "delete user", err)
		return
	}
	s.audit(r, actor, "user.delete", "user", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) unlockUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	actor, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := a.UnlockUser(r.Context(), id); err != nil {
		s.adminError(w, "unlock user", err)
		return
	}
	s.audit(r, actor, "user.unlock", "user", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"unlocked": true})
}

/* -------------------------------------------------------------------------- */
/* Audit log                                                                   */
/* -------------------------------------------------------------------------- */

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	a, ok := s.admin(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AuditFilter{
		Action: strings.TrimSpace(q.Get("action")),
		UserID: strings.TrimSpace(q.Get("user")),
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		f.Before = n
	}
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.Since = &t
		}
	}
	page, err := a.AuditLog(r.Context(), f)
	if err != nil {
		s.adminError(w, "read audit log", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

/* -------------------------------------------------------------------------- */
/* Audit writing and invitations                                               */
/* -------------------------------------------------------------------------- */

// audit records an administrative change against the acting user.
//
// Deliberately fire-and-forget. The change has already been committed and the
// caller has already been told it succeeded; failing the response now because the
// log insert failed would be a worse outcome than a visible gap in the log.
func (s *Server) audit(r *http.Request, actor, action, objType, objID string, detail map[string]any) {
	a, ok := s.store.(store.Administrator)
	if !ok {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	a.WriteAudit(r.Context(), actor, action, objType, objID, clientIP(r), detail)
}

// sendInvite issues a one-time link so an invited user can set their own password.
//
// It reuses the password reset machinery rather than generating a temporary
// password: a password an administrator knows is a password that has been
// transmitted in plain text somewhere, and it usually ends up in a chat message.
func (s *Server) sendInvite(ctx context.Context, email string) error {
	req, err := s.auth.CreateResetToken(ctx, email, "")
	if err != nil {
		return err
	}
	if !req.Found {
		return errors.New("user not found immediately after creation")
	}
	link := fmt.Sprintf("%s/reset?token=%s", strings.TrimRight(s.cfg.BaseURL, "/"), req.Token)
	return s.mailer.Send(inviteEmail(req.DisplayName, req.Email, link, req.ExpiresAt))
}

func inviteEmail(name, to, link string, expires time.Time) mail.Message {
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	valid := time.Until(expires).Round(time.Minute)

	text := fmt.Sprintf(`Hello %s,

You have been given access to NimbusEye, a monitoring console for cloud
infrastructure. Your account is %s.

Open this link to choose your password:

%s

The link is valid for %s and can be used once. If it expires before you get to
it, use "Forgot password" on the sign-in page to request another.

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
      You have been given access to NimbusEye. Your account is <strong>%s</strong>.
    </p>
    <p style="margin:22px 0">
      <a href="%s" style="display:inline-block;background:#2e8b46;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:6px;font-size:14px;font-weight:600">Choose your password</a>
    </p>
    <p style="font-size:12px;line-height:1.6;color:#64748b;margin:0">
      The link is valid for %s and can be used once. If it expires, use
      &ldquo;Forgot password&rdquo; on the sign-in page.
    </p>
    <p style="font-size:11px;color:#94a3b8;margin:20px 0 0;word-break:break-all">%s</p>
  </div>
</body></html>`, name, to, link, valid, link)

	return mail.Message{
		To:      []string{to},
		Subject: "Your NimbusEye account",
		Text:    text,
		HTML:    htmlBody,
	}
}

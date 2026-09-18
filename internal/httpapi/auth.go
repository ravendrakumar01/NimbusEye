package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nimbuseye/internal/auth"
)

// Session authentication for the API.
//
// The middleware is deny-by-default: every route is protected unless it appears in
// the public set below. Adding an endpoint therefore cannot accidentally expose
// data — forgetting to protect it is impossible, because protection is not
// something you remember to add.

type ctxKey int

const sessionKey ctxKey = 1

// publicPaths are reachable without a session.
//
// Deliberately short. /healthz carries no data beyond status and version, and the
// auth endpoints are how a session is obtained in the first place.
var publicPaths = map[string]bool{
	"/healthz":               true,
	"/api/v1/auth/login":     true,
	"/api/v1/auth/bootstrap": true,
	"/api/v1/auth/session":   true,
	// Reset is reached by someone who cannot sign in, so it cannot require a
	// session. Both endpoints are rate limited at nginx, and neither discloses
	// whether an address has an account.
	"/api/v1/auth/forgot": true,
	"/api/v1/auth/reset":  true,
}

// SessionFrom returns the authenticated session, if any.
func SessionFrom(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(sessionKey).(auth.Session)
	return s, ok
}

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/session", s.currentSession)
	mux.HandleFunc("POST /api/v1/auth/password", s.changePassword)
	mux.HandleFunc("GET /api/v1/auth/bootstrap", s.bootstrapStatus)
}

// requireSession enforces authentication and CSRF on everything not public.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			// No auth store configured. Refusing rather than allowing: an API that
			// silently serves everything because a dependency is missing is the
			// worst possible failure mode for this particular door.
			writeError(w, http.StatusServiceUnavailable, "auth_unavailable",
				"Authentication is not configured on this server")
			return
		}

		path := r.URL.Path
		// The collector's ingest endpoints authenticate with their own shared
		// token, checked by their own middleware.
		if publicPaths[path] || strings.HasPrefix(path, "/api/v1/ingest/") {
			next.ServeHTTP(w, r)
			return
		}

		session, err := s.auth.Validate(r.Context(), auth.TokenFromRequest(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue")
			return
		}

		// CSRF on state-changing methods. SameSite=Lax already blocks the
		// cross-site form POST, so this is the second layer: an attacker who can
		// make the browser send the cookie still cannot read it to echo it back.
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			cookie, _ := r.Cookie(auth.CSRFCookie)
			var cookieVal string
			if cookie != nil {
				cookieVal = cookie.Value
			}
			if !auth.SameCSRF(cookieVal, r.Header.Get(auth.CSRFHeader)) {
				s.log.Warn("csrf rejected", "path", path, "remote", r.RemoteAddr)
				writeError(w, http.StatusForbidden, "csrf_failed",
					"Request could not be verified. Reload the page and try again.")
				return
			}
		}

		// The session's tenant must be the one this process serves. Until the store
		// resolves a tenant per request, a mismatch means the deployment is
		// misconfigured, and serving another tenant's data would be the failure.
		if s.cfg.TenantSlug != "" && session.User.TenantSlug != s.cfg.TenantSlug {
			s.log.Error("session tenant does not match the configured tenant",
				"session_tenant", session.User.TenantSlug, "configured", s.cfg.TenantSlug)
			writeError(w, http.StatusForbidden, "tenant_mismatch",
				"This session belongs to a different tenant")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, session)))
	})
}

/* ------------------------------------------------------------- handlers */

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Body must be JSON")
		return
	}
	if strings.TrimSpace(body.Email) == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "missing_credentials", "Email and password are required")
		return
	}

	token, session, err := s.auth.Login(r.Context(), body.Email, body.Password,
		clientIP(r), r.Header.Get("User-Agent"))
	if err != nil {
		var locked *auth.LockedError
		if errors.As(err, &locked) {
			// The remaining time is disclosed on purpose: it is not a secret, and
			// without it the only option is to keep retrying, which extends the
			// lock.
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"code":                "account_locked",
					"message":             "Too many failed attempts. Try again shortly.",
					"retry_after_seconds": int(time.Until(locked.Until).Seconds()) + 1,
				},
			})
			return
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			// One message for both causes, so the form cannot be used to discover
			// which addresses have accounts.
			writeError(w, http.StatusUnauthorized, "invalid_credentials",
				"Email or password is incorrect")
			return
		}
		s.log.Error("login failed", "err", err)
		writeError(w, http.StatusInternalServerError, "login_failed", "Could not sign in")
		return
	}

	csrf, err := auth.NewCSRFToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login_failed", "Could not sign in")
		return
	}
	auth.SetSessionCookies(w, token, csrf, session.ExpiresAt, s.cfg.SecureCookies)

	s.log.Info("signed in", "user", session.User.Email, "role", session.User.Role, "ip", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       session.User,
		"expires_at": session.ExpiresAt,
		"csrf_token": csrf,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromRequest(r)
	if err := s.auth.Logout(r.Context(), token, clientIP(r)); err != nil {
		s.log.Error("logout failed", "err", err)
	}
	auth.ClearSessionCookies(w, s.cfg.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

// currentSession is public so the frontend can ask "am I signed in" on load
// without treating a 401 as an error worth showing.
func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.auth.Validate(r.Context(), auth.TokenFromRequest(r))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	var csrf string
	if c, err := r.Cookie(auth.CSRFCookie); err == nil {
		csrf = c.Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          session.User,
		"expires_at":    session.ExpiresAt,
		"csrf_token":    csrf,
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue")
		return
	}
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "Body must be JSON")
		return
	}
	if err := s.auth.ChangePassword(r.Context(), session.User.ID, body.Current, body.New, clientIP(r)); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "Current password is incorrect")
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]any{
				"code":    "validation_failed",
				"message": "Password could not be changed",
				"fields":  map[string]string{"new_password": userFacing(err)},
			},
		})
		return
	}
	// Every session was revoked, including this one, so the cookies must go too.
	auth.ClearSessionCookies(w, s.cfg.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]any{
		"changed": true,
		"message": "Password changed. All sessions were signed out.",
	})
}

// bootstrapStatus reports whether any user exists yet, so the login page can say
// "no accounts exist, create one with the CLI" instead of failing silently.
func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.auth.CountUsers(r.Context(), s.cfg.TenantSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bootstrap_check_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": n, "needs_bootstrap": n == 0})
}

// clientIP prefers the address nginx forwarded, since the API only ever sees the
// proxy's own address otherwise.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Left-most entry is the original client.
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
	}
	return r.RemoteAddr
}

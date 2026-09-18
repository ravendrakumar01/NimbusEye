package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds the database access auth needs.
//
// It talks to PostgreSQL directly rather than through the resource store, because
// the two pre-auth functions are the only queries in the system that must run
// without a tenant context and they are deliberately narrow.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// User is the authenticated identity.
type User struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	TenantSlug  string `json:"tenant_slug"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Timezone    string `json:"timezone,omitempty"`
	MFAEnabled  bool   `json:"mfa_enabled"`
}

// Session is a live sign-in.
type Session struct {
	ID        string
	User      User
	ExpiresAt time.Time
}

var (
	// ErrInvalidCredentials covers unknown address and wrong password alike. The
	// caller must not distinguish them: doing so turns the login form into an
	// account enumeration oracle.
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	// ErrLocked means too many failed attempts.
	ErrLocked = errors.New("auth: account is temporarily locked")
	// ErrNoSession means no valid session cookie was presented.
	ErrNoSession = errors.New("auth: no valid session")
)

// LockedError carries how long the lock lasts, so the UI can say something useful.
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string {
	return fmt.Sprintf("auth: account locked until %s", e.Until.Format(time.RFC3339))
}
func (e *LockedError) Unwrap() error { return ErrLocked }

// SessionLifetime is how long a sign-in lasts.
//
// Absolute, with no sliding renewal: a session that extends itself indefinitely on
// activity is a session that never ends, which is the wrong default for a tool
// holding cloud credentials.
const SessionLifetime = 12 * time.Hour

// Login verifies credentials and creates a session.
//
// The work is deliberately uniform: an unknown address still pays for a full hash
// comparison, and both failure modes return the same error.
func (s *Store) Login(ctx context.Context, email, password, ip, userAgent string) (string, Session, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	var (
		userID, tenantID, tenantSlug, hash, role, status string
		mfaEnabled                                       bool
		failedLogins                                     int
		lockedUntil                                      *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT user_id::text, tenant_id::text, tenant_slug::text,
		        coalesce(password_hash,''), role, status, mfa_enabled,
		        failed_logins, locked_until
		 FROM auth_lookup_user($1)`, email).
		Scan(&userID, &tenantID, &tenantSlug, &hash, &role, &status,
			&mfaEnabled, &failedLogins, &lockedUntil)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		VerifyAgainstDummy(password)
		return "", Session{}, ErrInvalidCredentials
	case err != nil:
		return "", Session{}, fmt.Errorf("auth: lookup: %w", err)
	}

	// Lockout is checked before the password, so a locked account cannot be
	// probed by continuing to guess.
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		VerifyAgainstDummy(password)
		return "", Session{}, &LockedError{Until: *lockedUntil}
	}
	if hash == "" {
		// An invited user who has not set a password yet.
		VerifyAgainstDummy(password)
		return "", Session{}, ErrInvalidCredentials
	}

	if err := VerifyPassword(hash, password); err != nil {
		_, _ = s.pool.Exec(ctx, `SELECT auth_record_attempt($1::uuid, false)`, userID)
		s.audit(ctx, tenantID, userID, "login.failure", ip, map[string]any{"email": email})
		return "", Session{}, ErrInvalidCredentials
	}

	if _, err := s.pool.Exec(ctx, `SELECT auth_record_attempt($1::uuid, true)`, userID); err != nil {
		return "", Session{}, fmt.Errorf("auth: record attempt: %w", err)
	}

	token, tokenHash, err := NewToken()
	if err != nil {
		return "", Session{}, err
	}
	expires := time.Now().UTC().Add(SessionLifetime)

	var sessionID, displayName, timezone string
	if err := s.pool.QueryRow(ctx,
		`SELECT session_id::text, display_name, timezone
		 FROM auth_create_session($1::uuid, $2::uuid, $3, $4::inet, $5, $6)`,
		tenantID, userID, tokenHash, nullIfEmptyInet(ip), userAgent, expires).
		Scan(&sessionID, &displayName, &timezone); err != nil {
		return "", Session{}, fmt.Errorf("auth: create session: %w", err)
	}

	s.audit(ctx, tenantID, userID, "login.success", ip, nil)

	return token, Session{
		ID:        sessionID,
		ExpiresAt: expires,
		User: User{
			ID: userID, TenantID: tenantID, TenantSlug: tenantSlug,
			Email: email, DisplayName: displayName, Role: role,
			Timezone: timezone, MFAEnabled: mfaEnabled,
		},
	}, nil
}

// Validate resolves a session token.
//
// Expiry and revocation are enforced in the query, so a revoked session cannot be
// used even if it is still cached anywhere. The user's status is joined in for the
// same reason: disabling a user takes effect on their next request rather than
// when their session happens to expire.
func (s *Store) Validate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	var (
		sessionID, userID, tenantID, tenantSlug string
		email, displayName, role, timezone      string
		mfaEnabled                              bool
		expires                                 time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT session_id::text, expires_at, user_id::text, tenant_id::text,
		        tenant_slug::text, email::text, display_name, role, timezone, mfa_enabled
		 FROM auth_validate_session($1)`, HashToken(token)).
		Scan(&sessionID, &expires, &userID, &tenantID, &tenantSlug,
			&email, &displayName, &role, &timezone, &mfaEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("auth: validate: %w", err)
	}
	return Session{
		ID: sessionID, ExpiresAt: expires,
		User: User{
			ID: userID, TenantID: tenantID, TenantSlug: tenantSlug,
			Email: email, DisplayName: displayName, Role: role,
			Timezone: timezone, MFAEnabled: mfaEnabled,
		},
	}, nil
}

// Logout revokes one session.
func (s *Store) Logout(ctx context.Context, token, ip string) error {
	var tenantID, userID string
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text, user_id::text FROM auth_revoke_session($1)`,
		HashToken(token)).Scan(&tenantID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already gone. Logging out twice is not an error worth surfacing.
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	s.audit(ctx, tenantID, userID, "logout", ip, nil)
	return nil
}

// RevokeAllForUser signs a user out everywhere. Used when a password changes.
func (s *Store) RevokeAllForUser(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `SELECT auth_revoke_user_sessions($1::uuid)`, userID)
	return err
}

// ChangePassword sets a new password and invalidates every existing session.
//
// Revoking sessions is the point: a password change usually means the old one is
// believed compromised, and leaving sessions alive would defeat it.
func (s *Store) ChangePassword(ctx context.Context, userID, current, next, ip string) error {
	var hash, tenantID string
	if err := s.pool.QueryRow(ctx,
		`SELECT password_hash, tenant_id::text FROM auth_get_password_hash($1::uuid)`,
		userID).Scan(&hash, &tenantID); err != nil {
		return fmt.Errorf("auth: load user: %w", err)
	}
	if hash == "" || VerifyPassword(hash, current) != nil {
		return ErrInvalidCredentials
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `SELECT auth_set_password($1::uuid, $2)`, userID, newHash); err != nil {
		return fmt.Errorf("auth: update password: %w", err)
	}
	if err := s.RevokeAllForUser(ctx, userID); err != nil {
		return err
	}
	s.audit(ctx, tenantID, userID, "password.change", ip, nil)
	return nil
}

// CreateUser provisions a user. Used by the bootstrap CLI and later by Admin.
func (s *Store) CreateUser(ctx context.Context, tenantSlug, email, displayName, role, password string) (string, error) {
	switch role {
	case "owner", "admin", "operator", "viewer":
	default:
		return "", fmt.Errorf("auth: unknown role %q", role)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	var tenantID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM tenant_by_slug($1)`, tenantSlug).
		Scan(&tenantID); err != nil {
		return "", fmt.Errorf("auth: tenant %q not found: %w", tenantSlug, err)
	}
	var id string
	if err := s.pool.QueryRow(ctx,
		`SELECT auth_upsert_user($1::uuid, $2, $3, $4, $5)::text`,
		tenantID, strings.ToLower(strings.TrimSpace(email)), displayName, hash, role).
		Scan(&id); err != nil {
		return "", fmt.Errorf("auth: create user: %w", err)
	}
	return id, nil
}

// CountUsers reports how many active users a tenant has, so the API can tell
// whether it still needs bootstrapping.
func (s *Store) CountUsers(ctx context.Context, tenantSlug string) (int, error) {
	var tenantID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM tenant_by_slug($1)`, tenantSlug).
		Scan(&tenantID); err != nil {
		return 0, fmt.Errorf("auth: tenant %q not found: %w", tenantSlug, err)
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT auth_count_users($1::uuid)`, tenantID).Scan(&n)
	return n, err
}

// Session cleanup is not implemented here. A DELETE on `sessions` is blocked by
// row-level security without a tenant context, and expired sessions are already
// inert — auth_validate_session refuses them. Reclaiming the rows is housekeeping
// that belongs in a scheduled job running as the schema owner, not in a method
// that would fail the moment it was called.

// audit appends to the audit log. Failures are swallowed on purpose: losing an
// audit row must not turn a successful login into an error for the user.
func (s *Store) audit(ctx context.Context, tenantID, userID, action, ip string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	_, _ = s.pool.Exec(ctx,
		`SELECT auth_write_audit($1::uuid, $2::uuid, $3, $4, $5::inet)`,
		tenantID, userID, action, detail, nullIfEmptyInet(ip))
}

// nullIfEmptyInet keeps an unparseable address out of an inet column.
func nullIfEmptyInet(ip string) *string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if net.ParseIP(ip) == nil {
		return nil
	}
	return &ip
}

/* ------------------------------------------------------------- cookies */

const (
	// SessionCookie holds the opaque session token. HttpOnly, so script cannot
	// read it even if a cross-site scripting bug is found.
	SessionCookie = "nimbuseye_session"
	// CSRFCookie holds the double-submit value. Deliberately readable by script,
	// because the frontend has to echo it back in a header.
	CSRFCookie = "nimbuseye_csrf"
	// CSRFHeader is where the echoed value is expected.
	CSRFHeader = "X-CSRF-Token"
)

// SetSessionCookies writes the session and CSRF cookies.
//
// secure is false only for plain-HTTP local development; a Secure cookie is never
// sent over HTTP, so hardcoding true would make local sign-in impossible and
// invite someone to remove the flag entirely.
func SetSessionCookies(w http.ResponseWriter, token, csrf string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		// Lax rather than Strict: Strict would drop the cookie when arriving from
		// an external link, logging the user out for no security gain here. Lax
		// still blocks the cross-site POST that CSRF depends on.
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    csrf,
		Path:     "/",
		Expires:  expires,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookies expires both cookies.
func ClearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/",
			Expires: time.Unix(0, 0), MaxAge: -1,
			HttpOnly: name == SessionCookie, Secure: secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// TokenFromRequest reads the session token from the request cookie.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

/* -------------------------------------------------------- password reset */

// ResetLifetime is how long a reset link stays valid.
//
// One hour: long enough to find the email, short enough that a link sitting in an
// unattended mailbox stops being useful quickly.
const ResetLifetime = time.Hour

// ResetRequest is what a caller needs to send the email. Zero value means no
// matching account, which the caller must not disclose.
type ResetRequest struct {
	Found       bool
	UserID      string
	Email       string
	DisplayName string
	Token       string
	ExpiresAt   time.Time
}

// CreateResetToken issues a reset token for an address.
//
// Returns Found=false for an unknown or inactive account, with no error: the caller
// responds identically either way, so the form cannot be used to discover which
// addresses have accounts.
func (s *Store) CreateResetToken(ctx context.Context, email, ip string) (ResetRequest, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ResetRequest{}, nil
	}

	token, hash, err := NewToken()
	if err != nil {
		return ResetRequest{}, err
	}
	expires := time.Now().UTC().Add(ResetLifetime)

	var userID, displayName, storedEmail string
	err = s.pool.QueryRow(ctx,
		`SELECT user_id::text, display_name, email::text
		 FROM auth_create_reset_token($1, $2, $3, $4::inet)`,
		email, hash, expires, nullIfEmptyInet(ip)).
		Scan(&userID, &displayName, &storedEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetRequest{}, nil
	}
	if err != nil {
		return ResetRequest{}, fmt.Errorf("auth: create reset token: %w", err)
	}

	return ResetRequest{
		Found: true, UserID: userID, Email: storedEmail,
		DisplayName: displayName, Token: token, ExpiresAt: expires,
	}, nil
}

// RedeemResetToken consumes a token and sets the new password.
//
// The token check and the password write happen in one database call, so the same
// link cannot be redeemed twice through a race.
func (s *Store) RedeemResetToken(ctx context.Context, token, newPassword, ip string) error {
	if token == "" {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	var userID, tenantID, email string
	err = s.pool.QueryRow(ctx,
		`SELECT user_id::text, tenant_id::text, email::text
		 FROM auth_redeem_reset_token($1, $2)`, HashToken(token), hash).
		Scan(&userID, &tenantID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		// Expired, already used, or never existed. One error for all three: telling
		// them apart helps nobody except someone probing tokens.
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("auth: redeem reset token: %w", err)
	}

	s.audit(ctx, tenantID, userID, "password.reset", ip, map[string]any{"email": email})
	return nil
}

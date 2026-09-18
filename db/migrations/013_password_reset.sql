-- NimbusEye — password reset tokens.
--
-- Single-use, short-lived, and stored only as a hash, for the same reason session
-- tokens are: a database dump must not yield a working reset link.
--
-- The functions are SECURITY DEFINER because a reset request arrives with no
-- session and therefore no tenant context — the same pre-authentication problem
-- solved for login and session validation. Each is keyed on a value the caller
-- must already hold: an email address it is not told the validity of, or a token
-- that is 256 bits of CSPRNG output.

BEGIN;

CREATE TABLE password_reset_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    -- Set when redeemed, so a link cannot be replayed from a mailbox or a proxy
    -- log after it has been used once.
    used_at    timestamptz,
    requested_ip inet,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_user_idx ON password_reset_tokens (user_id, created_at DESC);
CREATE INDEX password_reset_expiry_idx ON password_reset_tokens (expires_at) WHERE used_at IS NULL;

ALTER TABLE password_reset_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON password_reset_tokens
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON password_reset_tokens TO nimbuseye_app;

-- Issue a token for an email address.
--
-- Returns no row when the address is unknown or inactive, and the caller is
-- expected to respond identically either way: a reset form that reveals whether an
-- address is registered is an account enumeration tool.
--
-- Any outstanding tokens for the user are invalidated first, so requesting a second
-- link cannot leave two live ones.
CREATE OR REPLACE FUNCTION auth_create_reset_token(
    p_email      citext,
    p_token_hash bytea,
    p_expires_at timestamptz,
    p_ip         inet
)
RETURNS TABLE (user_id uuid, display_name text, email citext)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    u RECORD;
BEGIN
    SELECT usr.id, usr.tenant_id, usr.display_name, usr.email
      INTO u
      FROM users usr
      JOIN tenants t ON t.id = usr.tenant_id
     WHERE usr.email = p_email
       AND usr.status = 'active'
       AND t.status = 'active'
     LIMIT 1;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE password_reset_tokens
       SET used_at = now()
     WHERE password_reset_tokens.user_id = u.id AND used_at IS NULL;

    INSERT INTO password_reset_tokens
        (tenant_id, user_id, token_hash, expires_at, requested_ip)
    VALUES (u.tenant_id, u.id, p_token_hash, p_expires_at, p_ip);

    RETURN QUERY SELECT u.id, u.display_name, u.email;
END $$;

REVOKE ALL ON FUNCTION auth_create_reset_token(citext, bytea, timestamptz, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_create_reset_token(citext, bytea, timestamptz, inet) TO nimbuseye_app;

-- Redeem a token and set the new password, in one transaction.
--
-- Combined on purpose: checking validity and consuming the token in separate calls
-- leaves a window where the same link works twice. Every session is revoked too,
-- because a reset means the account may have been compromised.
CREATE OR REPLACE FUNCTION auth_redeem_reset_token(p_token_hash bytea, p_new_hash text)
RETURNS TABLE (user_id uuid, tenant_id uuid, email citext)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    t RECORD;
BEGIN
    SELECT prt.id, prt.user_id, prt.tenant_id
      INTO t
      FROM password_reset_tokens prt
     WHERE prt.token_hash = p_token_hash
       AND prt.used_at IS NULL
       AND prt.expires_at > now()
     FOR UPDATE;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE password_reset_tokens SET used_at = now() WHERE id = t.id;

    UPDATE users
       SET password_hash = p_new_hash,
           -- A successful reset clears any lockout: the person proved control of
           -- the mailbox, and leaving them locked out would be pointless.
           failed_logins = 0,
           locked_until  = NULL,
           updated_at    = now()
     WHERE id = t.user_id;

    UPDATE sessions SET revoked_at = now()
     WHERE sessions.user_id = t.user_id AND revoked_at IS NULL;

    RETURN QUERY
        SELECT u.id, u.tenant_id, u.email FROM users u WHERE u.id = t.user_id;
END $$;

REVOKE ALL ON FUNCTION auth_redeem_reset_token(bytea, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_redeem_reset_token(bytea, text) TO nimbuseye_app;

COMMIT;

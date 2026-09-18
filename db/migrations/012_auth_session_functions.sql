-- NimbusEye — session operations that must run before a tenant is known.
--
-- Sessions are tenant-scoped and protected by row-level security, but validating
-- a session is what establishes which tenant the request belongs to. The lookup
-- therefore cannot itself be tenant-scoped — the same chicken-and-egg that
-- auth_lookup_user and tenant_by_slug already solve.
--
-- The answer is the same and stays deliberately narrow: SECURITY DEFINER functions
-- that each do exactly one thing, keyed on a value the caller must already
-- possess. A session token hash is 256 bits of CSPRNG output, so presenting one is
-- itself the proof of authorisation; these functions cannot be used to enumerate
-- anything.
--
-- The alternative would be granting the application BYPASSRLS, which would discard
-- tenant isolation for every query in the system to solve a problem in four.

BEGIN;

-- Resolve a session. Expiry, revocation and the user's and tenant's status are all
-- enforced here, so a disabled user's existing session stops working on their next
-- request rather than when it happens to expire.
CREATE OR REPLACE FUNCTION auth_validate_session(p_token_hash bytea)
RETURNS TABLE (
    session_id   uuid,
    expires_at   timestamptz,
    user_id      uuid,
    tenant_id    uuid,
    tenant_slug  citext,
    email        citext,
    display_name text,
    role         text,
    timezone     text,
    mfa_enabled  boolean
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
STABLE
AS $$
    SELECT ss.id, ss.expires_at, u.id, u.tenant_id, t.slug,
           u.email, u.display_name, u.role, coalesce(u.timezone, ''), u.mfa_enabled
    FROM sessions ss
    JOIN users u   ON u.id = ss.user_id
    JOIN tenants t ON t.id = u.tenant_id
    WHERE ss.token_hash = p_token_hash
      AND ss.revoked_at IS NULL
      AND ss.expires_at > now()
      AND u.status = 'active'
      AND t.status = 'active'
$$;

REVOKE ALL ON FUNCTION auth_validate_session(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_validate_session(bytea) TO nimbuseye_app;

-- Revoke one session. Returns who it belonged to so the caller can write an audit
-- entry; without that the tenant would be unknowable after the row is revoked.
CREATE OR REPLACE FUNCTION auth_revoke_session(p_token_hash bytea)
RETURNS TABLE (tenant_id uuid, user_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    UPDATE sessions SET revoked_at = now()
    WHERE token_hash = p_token_hash AND revoked_at IS NULL
    RETURNING tenant_id, user_id
$$;

REVOKE ALL ON FUNCTION auth_revoke_session(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_revoke_session(bytea) TO nimbuseye_app;

-- Sign a user out everywhere. Used when a password changes, where leaving old
-- sessions alive would defeat the point of changing it.
CREATE OR REPLACE FUNCTION auth_revoke_user_sessions(p_user_id uuid)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n integer;
BEGIN
    UPDATE sessions SET revoked_at = now()
     WHERE user_id = p_user_id AND revoked_at IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END $$;

REVOKE ALL ON FUNCTION auth_revoke_user_sessions(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_revoke_user_sessions(uuid) TO nimbuseye_app;

-- Create a session. The caller already knows the tenant, having just
-- authenticated the user, but the INSERT still needs a tenant context it cannot
-- set from a pooled connection without a transaction. Doing it here keeps login to
-- a single round trip.
CREATE OR REPLACE FUNCTION auth_create_session(
    p_tenant_id  uuid,
    p_user_id    uuid,
    p_token_hash bytea,
    p_ip         inet,
    p_user_agent text,
    p_expires_at timestamptz
)
RETURNS TABLE (session_id uuid, display_name text, timezone text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    sid uuid;
BEGIN
    -- The user must belong to the tenant being claimed. Without this check a
    -- caller could mint a session binding any user to any tenant.
    PERFORM 1 FROM users
     WHERE id = p_user_id AND tenant_id = p_tenant_id AND status = 'active';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'user does not belong to that tenant';
    END IF;

    INSERT INTO sessions (tenant_id, user_id, token_hash, ip, user_agent,
                          mfa_satisfied, expires_at)
    VALUES (p_tenant_id, p_user_id, p_token_hash, p_ip, p_user_agent, true, p_expires_at)
    RETURNING id INTO sid;

    RETURN QUERY
        SELECT sid, u.display_name, coalesce(u.timezone, '')
        FROM users u WHERE u.id = p_user_id;
END $$;

REVOKE ALL ON FUNCTION auth_create_session(uuid, uuid, bytea, inet, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_create_session(uuid, uuid, bytea, inet, text, timestamptz) TO nimbuseye_app;

-- Append an audit entry. Needed pre-context so a failed login — where no session
-- exists and no tenant context is set — is still recorded.
CREATE OR REPLACE FUNCTION auth_write_audit(
    p_tenant_id uuid,
    p_user_id   uuid,
    p_action    text,
    p_detail    jsonb,
    p_ip        inet
)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    INSERT INTO audit_log (tenant_id, user_id, action, detail, ip)
    VALUES (p_tenant_id, p_user_id, p_action, coalesce(p_detail, '{}'::jsonb), p_ip)
$$;

REVOKE ALL ON FUNCTION auth_write_audit(uuid, uuid, text, jsonb, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_write_audit(uuid, uuid, text, jsonb, inet) TO nimbuseye_app;

-- Read and change a user's own password. Scoped to one user id, which the caller
-- can only have obtained from a validated session.
CREATE OR REPLACE FUNCTION auth_get_password_hash(p_user_id uuid)
RETURNS TABLE (password_hash text, tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
STABLE
AS $$
    SELECT coalesce(u.password_hash, ''), u.tenant_id
    FROM users u WHERE u.id = p_user_id AND u.status = 'active'
$$;

CREATE OR REPLACE FUNCTION auth_set_password(p_user_id uuid, p_hash text)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    UPDATE users SET password_hash = p_hash, updated_at = now()
    WHERE id = p_user_id AND status = 'active'
$$;

REVOKE ALL ON FUNCTION auth_get_password_hash(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_set_password(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_get_password_hash(uuid) TO nimbuseye_app;
GRANT EXECUTE ON FUNCTION auth_set_password(uuid, text) TO nimbuseye_app;

-- Provisioning: create or reset a user. Administration, so it is granted to the
-- app role only because the bootstrap CLI runs with the application DSN.
CREATE OR REPLACE FUNCTION auth_upsert_user(
    p_tenant_id    uuid,
    p_email        citext,
    p_display_name text,
    p_hash         text,
    p_role         text
)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    uid uuid;
BEGIN
    IF p_role NOT IN ('owner', 'admin', 'operator', 'viewer') THEN
        RAISE EXCEPTION 'unknown role %', p_role;
    END IF;
    INSERT INTO users (tenant_id, email, display_name, password_hash, role, status)
    VALUES (p_tenant_id, p_email, p_display_name, p_hash, p_role, 'active')
    ON CONFLICT (tenant_id, email) DO UPDATE
      SET password_hash = EXCLUDED.password_hash,
          display_name  = EXCLUDED.display_name,
          role          = EXCLUDED.role,
          status        = 'active',
          failed_logins = 0,
          locked_until  = NULL,
          updated_at    = now()
    RETURNING id INTO uid;
    RETURN uid;
END $$;

CREATE OR REPLACE FUNCTION auth_count_users(p_tenant_id uuid)
RETURNS integer
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
STABLE
AS $$
    SELECT count(*)::integer FROM users
    WHERE tenant_id = p_tenant_id AND status = 'active'
$$;

REVOKE ALL ON FUNCTION auth_upsert_user(uuid, citext, text, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_count_users(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_upsert_user(uuid, citext, text, text, text) TO nimbuseye_app;
GRANT EXECUTE ON FUNCTION auth_count_users(uuid) TO nimbuseye_app;

COMMIT;

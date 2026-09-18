-- NimbusEye — row-level security
-- Tenant isolation is enforced in the database, not only in application code.
-- A missing WHERE tenant_id = ... in one handler is then a bug that returns
-- nothing, rather than a cross-tenant data leak. This matters more here than
-- usual: the app is internet-facing and holds a full cloud inventory.
--
-- The application connects as role `nimbuseye_app` and issues
--   SET LOCAL nimbuseye.tenant_id = '<uuid>';
-- inside every request transaction. Unset means no rows are visible.

BEGIN;

-- Application role. Deliberately not the table owner and not superuser:
-- RLS is bypassed by owners and superusers, which would defeat the point.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nimbuseye_app') THEN
        CREATE ROLE nimbuseye_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nimbuseye_migrator') THEN
        CREATE ROLE nimbuseye_migrator NOLOGIN;
    END IF;
END $$;

-- Apply an identical tenant policy to every tenant-scoped table.
DO $$
DECLARE
    t text;
    tenant_tables text[] := ARRAY[
        'users','sessions','user_groups','audit_log',
        'cloud_accounts','resources','resource_groups','tags',
        'threshold_profiles','business_hours','notification_channels',
        'notification_profiles','oncall_schedules','oncall_shifts',
        'maintenance_windows','alerts','alert_notifications','outages',
        'availability_daily','sla_definitions','collector_jobs',
        'cost_sources','cost_import_runs','cost_centers','budgets',
        'budget_evaluations'
    ];
BEGIN
    FOREACH t IN ARRAY tenant_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = current_tenant_id())
            WITH CHECK (tenant_id = current_tenant_id())
        $f$, t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO nimbuseye_app', t);
    END LOOP;
END $$;

-- cost_line_items is partitioned; policy must be declared on the parent and is
-- inherited by every partition.
ALTER TABLE cost_line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_line_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cost_line_items
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON cost_line_items TO nimbuseye_app;

-- The tenants row itself: a tenant may read and update only its own record.
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_self ON tenants
    USING (id = current_tenant_id())
    WITH CHECK (id = current_tenant_id());
GRANT SELECT, UPDATE ON tenants TO nimbuseye_app;

-- Join tables carry no tenant_id of their own. They are constrained through
-- their parent, which is already tenant-filtered by RLS.
GRANT SELECT, INSERT, UPDATE, DELETE ON
    user_group_members, resource_group_members, resource_tags TO nimbuseye_app;

-- Global catalog: readable by the app, writable only by migrations.
GRANT SELECT ON resource_types TO nimbuseye_app;

-- Sequences used by bigserial columns.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO nimbuseye_app;

-- Audit log is append-only for the application. Even a compromised app session
-- cannot erase its own tracks.
REVOKE UPDATE, DELETE ON audit_log FROM nimbuseye_app;

-- Materialised view for the cost dashboard. RLS does not apply to matviews, so
-- it is exposed through a security-barrier view that filters by tenant.
CREATE VIEW cost_daily_summary_v WITH (security_barrier) AS
    SELECT * FROM cost_daily_summary WHERE tenant_id = current_tenant_id();
GRANT SELECT ON cost_daily_summary_v TO nimbuseye_app;
REVOKE ALL ON cost_daily_summary FROM nimbuseye_app;

-- ---------------------------------------------------------------------------
-- Pre-authentication lookup.
--
-- RLS creates a chicken-and-egg problem at login: the tenant is not known until
-- the user is identified, but with nimbuseye.tenant_id unset no row is visible.
-- Granting the app BYPASSRLS to work around it would discard tenant isolation
-- entirely, so instead this narrowly-scoped SECURITY DEFINER function is the
-- only pre-auth read path.
--
-- It returns the password hash, so it must stay minimal and be the single
-- exception. Note what it deliberately does not do: no wildcard search, no
-- listing, one row for one exact address. It also does not reveal whether an
-- address exists in a way the caller can distinguish from a wrong password —
-- the caller is expected to run the Argon2 verification regardless, against a
-- dummy hash when no row is returned, to keep response timing flat.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION auth_lookup_user(p_email citext)
RETURNS TABLE (
    user_id       uuid,
    tenant_id     uuid,
    tenant_slug   citext,
    password_hash text,
    role          text,
    status        text,
    mfa_enabled   boolean,
    failed_logins integer,
    locked_until  timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
STABLE
AS $$
    SELECT u.id, u.tenant_id, t.slug, u.password_hash, u.role, u.status,
           u.mfa_enabled, u.failed_logins, u.locked_until
    FROM users u
    JOIN tenants t ON t.id = u.tenant_id
    WHERE u.email = p_email
      AND u.status = 'active'
      AND t.status = 'active'
    LIMIT 1
$$;

REVOKE ALL ON FUNCTION auth_lookup_user(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_user(citext) TO nimbuseye_app;

-- Login outcome recording, also pre-auth (failed attempts must be counted for a
-- user the caller cannot yet see). Scoped to one user id and two counters.
CREATE OR REPLACE FUNCTION auth_record_attempt(p_user_id uuid, p_success boolean)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF p_success THEN
        UPDATE users
           SET failed_logins = 0, locked_until = NULL, last_login_at = now()
         WHERE id = p_user_id;
    ELSE
        UPDATE users
           SET failed_logins = failed_logins + 1,
               -- Exponential lockout after 5 failures, capped at 30 minutes.
               locked_until = CASE
                   WHEN failed_logins + 1 >= 5
                   THEN now() + least(interval '30 minutes',
                                      interval '1 minute' * power(2, failed_logins - 3))
                   ELSE locked_until END
         WHERE id = p_user_id;
    END IF;
END $$;

REVOKE ALL ON FUNCTION auth_record_attempt(uuid, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_record_attempt(uuid, boolean) TO nimbuseye_app;

COMMIT;

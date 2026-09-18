-- NimbusEye — tenant resolution for the application role.
--
-- The application connects, then needs to learn its tenant id in order to set
-- `nimbuseye.tenant_id` for every subsequent query. But the policy on `tenants`
-- is `id = current_tenant_id()`, so with the setting still unset the app role
-- cannot see any tenant — including the one it is about to work as. Same
-- chicken-and-egg as the login path in 003, one level up.
--
-- The fix is the same narrow one: a SECURITY DEFINER function that is the only
-- pre-context read path. It exposes no secrets — a tenant row is a slug, a
-- display name and some formatting preferences — and it answers for one exact
-- slug rather than listing anything.
--
-- Once session authentication exists the tenant comes from the session and this
-- is used only at startup, to bind a single-tenant deployment to its tenant.

BEGIN;

CREATE OR REPLACE FUNCTION tenant_by_slug(p_slug citext)
RETURNS TABLE (
    id             uuid,
    slug           citext,
    display_name   text,
    timezone       text,
    currency       char(3),
    fy_start_month smallint
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
STABLE
AS $$
    SELECT t.id, t.slug, t.display_name, t.timezone, t.currency, t.fy_start_month
    FROM tenants t
    WHERE t.slug = p_slug
      AND t.status = 'active'
    LIMIT 1
$$;

REVOKE ALL ON FUNCTION tenant_by_slug(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tenant_by_slug(citext) TO nimbuseye_app;

COMMIT;

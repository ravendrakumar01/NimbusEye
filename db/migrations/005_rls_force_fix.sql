-- NimbusEye — correct the row-level security force setting.
--
-- 003_rls.sql applied FORCE ROW LEVEL SECURITY to every tenant-scoped table.
-- That was wrong, and running it revealed why: FORCE subjects the *table owner*
-- to RLS as well, so with `nimbuseye.tenant_id` unset the schema owner could
-- neither read nor write anything. In particular there was no way to create a
-- tenant at all — the first INSERT into `tenants` fails its own WITH CHECK,
-- because the row's id cannot equal a tenant id that does not exist yet.
--
-- What actually provides the isolation is the combination that is already in
-- place: RLS is enabled, and the application connects as `nimbuseye_app`, which
-- is neither the table owner nor a superuser and is explicitly NOBYPASSRLS. The
-- policies therefore apply to every application query. FORCE adds nothing on top
-- of that for the application, and costs the ability to operate the database:
-- backups, migrations, tenant provisioning and support queries all run as the
-- owner.
--
-- Dropping FORCE restores owner access while leaving application isolation
-- exactly as strong.

BEGIN;

DO $$
DECLARE
    t text;
    all_tables text[] := ARRAY[
        'tenants',
        'users','sessions','user_groups','audit_log',
        'cloud_accounts','resources','resource_groups','tags',
        'threshold_profiles','business_hours','notification_channels',
        'notification_profiles','oncall_schedules','oncall_shifts',
        'maintenance_windows','alerts','alert_notifications','outages',
        'availability_daily','sla_definitions','collector_jobs',
        'cost_sources','cost_import_runs','cost_centers','budgets',
        'budget_evaluations','cost_line_items'
    ];
BEGIN
    FOREACH t IN ARRAY all_tables LOOP
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;

-- Tenant provisioning is an administrative action, not something the
-- application performs on its own behalf, so the app role keeps only SELECT and
-- UPDATE on its own row. Creating and deleting tenants stays with the owner.
REVOKE INSERT, DELETE ON tenants FROM nimbuseye_app;

COMMIT;

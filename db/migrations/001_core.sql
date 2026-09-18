-- NimbusEye — core schema (multi-tenant cloud monitoring)
-- Target: PostgreSQL 16
-- Design notes:
--   * Every tenant-scoped table carries tenant_id and is protected by row-level security.
--     The application sets `SET LOCAL nimbuseye.tenant_id = '<uuid>'` per transaction.
--   * Time-series metric samples do NOT live here. They go to VictoriaMetrics.
--     PostgreSQL holds inventory, configuration, alert state and daily rollups.
--   * Thresholds bind to a resource_type, not to individual resources. One profile
--     governs hundreds of resources; this is what keeps config manageable at scale.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;     -- case-insensitive email

-- ---------------------------------------------------------------------------
-- Helper: resolve the current tenant from the session GUC.
-- Returns NULL when unset, which makes RLS policies deny by default.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION current_tenant_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('nimbuseye.tenant_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END $$;

-- ===========================================================================
-- 1. TENANCY AND IDENTITY
-- ===========================================================================

CREATE TABLE tenants (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         citext NOT NULL UNIQUE,
    display_name text   NOT NULL,
    status       text   NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active','suspended','deleted')),
    timezone     text   NOT NULL DEFAULT 'Asia/Kolkata',
    currency     char(3) NOT NULL DEFAULT 'INR',
    -- FinOps: fiscal year start month, 1 = January
    fy_start_month smallint NOT NULL DEFAULT 1
                 CHECK (fy_start_month BETWEEN 1 AND 12),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Roles are coarse on purpose. Fine-grained permissions are a later concern;
-- getting three clear levels right beats a half-built RBAC engine.
--   owner    : full control including billing and tenant settings
--   admin    : manage cloud accounts, profiles, users
--   operator : acknowledge alerts, run reports, no config changes
--   viewer   : read only
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email          citext NOT NULL,
    display_name   text   NOT NULL,
    -- Argon2id encoded hash. Never a plain or fast hash: this app is on the
    -- public internet and the password is the only thing in front of a full
    -- multi-cloud inventory.
    password_hash  text,
    role           text NOT NULL DEFAULT 'viewer'
                   CHECK (role IN ('owner','admin','operator','viewer')),
    status         text NOT NULL DEFAULT 'invited'
                   CHECK (status IN ('invited','active','disabled')),
    -- TOTP secret, encrypted at the application layer before storage.
    mfa_secret_enc bytea,
    mfa_enabled    boolean NOT NULL DEFAULT false,
    phone          text,
    timezone       text,
    last_login_at  timestamptz,
    -- Login throttling state. Prevents credential stuffing against a public login.
    failed_logins  integer NOT NULL DEFAULT 0,
    locked_until   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);
CREATE INDEX users_tenant_idx ON users (tenant_id) WHERE status <> 'disabled';

-- Server-side sessions. We store only a hash of the session token so a database
-- leak cannot be replayed as a live login.
CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE,
    ip            inet,
    user_agent    text,
    mfa_satisfied boolean NOT NULL DEFAULT false,
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expiry_idx  ON sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE user_groups (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name text NOT NULL,
    description  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE user_group_members (
    user_group_id uuid NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (user_group_id, user_id)
);

-- Append-only. Nothing in the application issues UPDATE or DELETE here.
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id     uuid REFERENCES users(id) ON DELETE SET NULL,
    action      text NOT NULL,          -- 'login.success', 'cloud_account.create', ...
    object_type text,
    object_id   text,
    detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip          inet,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant_time_idx ON audit_log (tenant_id, created_at DESC);

-- ===========================================================================
-- 2. CLOUD ACCOUNTS
-- ===========================================================================

-- Credentials are NOT stored in this table. `credentials_ref` points at an
-- external secret (file path on the server, or later a KMS/Vault key id) and the
-- collector resolves it at runtime. This keeps a database dump free of cloud keys.
CREATE TABLE cloud_accounts (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider              text NOT NULL CHECK (provider IN ('oci','aws','azure','gcp')),
    display_name          text NOT NULL,
    -- Native account identifier: OCI tenancy OCID, AWS account id,
    -- Azure subscription id, GCP project id.
    native_account_id     text NOT NULL,
    credentials_ref       text NOT NULL,
    -- Provider-specific non-secret settings: compartments, resource groups,
    -- assume-role ARN, quota tuning.
    config                jsonb NOT NULL DEFAULT '{}'::jsonb,
    regions               text[] NOT NULL DEFAULT '{}',
    enabled               boolean NOT NULL DEFAULT true,
    discovery_interval_sec integer NOT NULL DEFAULT 3600
                          CHECK (discovery_interval_sec >= 300),
    metric_interval_sec   integer NOT NULL DEFAULT 300
                          CHECK (metric_interval_sec >= 60),
    -- Observability of the collector itself. A silently broken integration is
    -- the failure mode that makes a monitoring tool worse than none: the OCI
    -- discovery in the evaluated account stalled without surfacing anywhere.
    last_discovery_at     timestamptz,
    last_discovery_status text CHECK (last_discovery_status IN
                            ('never','running','ok','partial','failed')) DEFAULT 'never',
    last_error            text,
    consecutive_failures  integer NOT NULL DEFAULT 0,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, provider, native_account_id)
);
CREATE INDEX cloud_accounts_due_idx ON cloud_accounts (last_discovery_at)
    WHERE enabled;

-- ===========================================================================
-- 3. RESOURCE CATALOG AND INVENTORY
-- ===========================================================================

-- Global (not tenant-scoped) catalog describing every monitorable type.
-- Adding support for a new cloud service is a row here plus a collector mapping,
-- not a schema migration. metric_defs drives the UI, the threshold editor and
-- the collector's metric query, so all three stay in sync from one definition.
CREATE TABLE resource_types (
    code                 text PRIMARY KEY,          -- 'OCI_COMPUTE_INSTANCE'
    provider             text NOT NULL CHECK (provider IN ('oci','aws','azure','gcp','k8s','synthetic')),
    category             text NOT NULL,             -- compute | storage | database | network | container | serverless | web
    display_name         text NOT NULL,
    icon                 text,
    supports_availability boolean NOT NULL DEFAULT true,
    supports_metrics     boolean NOT NULL DEFAULT true,
    supports_cost        boolean NOT NULL DEFAULT true,
    default_poll_sec     integer NOT NULL DEFAULT 300,
    -- [{ "key":"cpu_utilization", "label":"CPU Utilization", "unit":"percent",
    --    "provider_metric":"CpuUtilization", "namespace":"oci_computeagent",
    --    "statistic":"mean", "default_trouble":75, "default_critical":90 }]
    metric_defs          jsonb NOT NULL DEFAULT '[]'::jsonb,
    enabled              boolean NOT NULL DEFAULT true
);

-- The equivalent of a Site24x7 "monitor": one monitored thing.
-- Type-specific fields live in `attributes` rather than in per-type tables.
-- With 190+ resource types a table-per-type design becomes unmaintainable.
CREATE TABLE resources (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cloud_account_id   uuid REFERENCES cloud_accounts(id) ON DELETE CASCADE,
    resource_type      text NOT NULL REFERENCES resource_types(code),
    -- OCID / ARN / Azure resource id / GCP self-link. Stable identity across
    -- rediscovery; display names change, these do not.
    native_id          text NOT NULL,
    display_name       text NOT NULL,
    region             text,
    availability_zone  text,
    -- Parent/child, e.g. cluster -> node, bucket -> folder, DB cluster -> instance.
    parent_resource_id uuid REFERENCES resources(id) ON DELETE CASCADE,
    attributes         jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Current health. Kept denormalised for the Home dashboard, which must
    -- render 2000 resources without touching the time-series store.
    status             text NOT NULL DEFAULT 'discovery'
                       CHECK (status IN ('up','down','trouble','critical',
                                         'suspended','maintenance','discovery','unknown')),
    status_since       timestamptz NOT NULL DEFAULT now(),
    last_polled_at     timestamptz,
    last_poll_error    text,

    threshold_profile_id    uuid,
    notification_profile_id uuid,

    -- Suspended resources are kept but neither polled nor alerted on.
    suspended          boolean NOT NULL DEFAULT false,
    -- Set when discovery stops seeing the resource. Soft delete preserves
    -- historical availability and cost attribution.
    discovered_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, cloud_account_id, native_id)
);
CREATE INDEX resources_tenant_status_idx ON resources (tenant_id, status)
    WHERE deleted_at IS NULL;
CREATE INDEX resources_type_idx    ON resources (tenant_id, resource_type) WHERE deleted_at IS NULL;
CREATE INDEX resources_account_idx ON resources (cloud_account_id)         WHERE deleted_at IS NULL;
CREATE INDEX resources_poll_idx    ON resources (last_polled_at)
    WHERE deleted_at IS NULL AND NOT suspended;
CREATE INDEX resources_attrs_idx   ON resources USING gin (attributes);

-- ===========================================================================
-- 4. GROUPING AND TAGS
-- ===========================================================================

CREATE TABLE resource_groups (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name    text NOT NULL,
    description     text,
    parent_group_id uuid REFERENCES resource_groups(id) ON DELETE CASCADE,
    -- How a group's health is derived from its members.
    health_strategy text NOT NULL DEFAULT 'worst_child'
                    CHECK (health_strategy IN ('worst_child','percentage','count')),
    -- For 'percentage'/'count': how many unhealthy members trip the group.
    health_threshold integer,
    -- Non-null makes this a dynamic group: members are matched by rule at
    -- discovery time instead of being pinned manually. Essential at 2000
    -- resources, where hand-maintained membership rots within weeks.
    match_rules     jsonb,
    threshold_profile_id    uuid,
    notification_profile_id uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE resource_group_members (
    group_id    uuid NOT NULL REFERENCES resource_groups(id) ON DELETE CASCADE,
    resource_id uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    -- true when added by match_rules rather than by a person
    dynamic     boolean NOT NULL DEFAULT false,
    PRIMARY KEY (group_id, resource_id)
);
CREATE INDEX rgm_resource_idx ON resource_group_members (resource_id);

-- Cloud-imported tags and user tags share one table, distinguished by `source`.
-- The evaluated Site24x7 account did exactly this: OCI's freeform tags arrived
-- as Oracle-Tags.CreatedBy / Oracle-Tags.CreatedOn.
CREATE TABLE tags (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key       text NOT NULL,
    value     text NOT NULL DEFAULT '',
    source    text NOT NULL DEFAULT 'user' CHECK (source IN ('user','cloud')),
    color     text,
    UNIQUE (tenant_id, key, value, source)
);

CREATE TABLE resource_tags (
    tag_id      uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    resource_id uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    PRIMARY KEY (tag_id, resource_id)
);
CREATE INDEX resource_tags_resource_idx ON resource_tags (resource_id);

-- ===========================================================================
-- 5. PROFILES  (threshold / notification / business hours)
-- ===========================================================================

-- rules mirrors the primitive observed in the evaluated account:
-- [{ "metric":"cpu_utilization", "op":">=", "severity":"trouble",
--    "value":75, "polls_check":3, "strategy":"consecutive" }]
--
-- polls_check is the single most important field here. Alerting on one bad
-- sample produces constant false positives on cloud metrics, which arrive late
-- and occasionally out of order.
CREATE TABLE threshold_profiles (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name     text NOT NULL,
    resource_type    text NOT NULL REFERENCES resource_types(code),
    rules            jsonb NOT NULL DEFAULT '[]'::jsonb,
    -- Availability rules are separate from metric rules: "how many failed
    -- polls before the resource counts as down".
    down_polls_check integer NOT NULL DEFAULT 2 CHECK (down_polls_check >= 1),
    system_generated boolean NOT NULL DEFAULT false,
    is_default       boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);
-- Exactly one default profile per resource type per tenant.
CREATE UNIQUE INDEX threshold_default_uniq
    ON threshold_profiles (tenant_id, resource_type) WHERE is_default;

CREATE TABLE business_hours (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name text NOT NULL,
    timezone     text NOT NULL DEFAULT 'Asia/Kolkata',
    -- [{"day":1,"start":"09:00","end":"17:00"}]  ISO-8601 weekday: 1=Mon .. 7=Sun
    time_config  jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE notification_channels (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel_type text NOT NULL CHECK (channel_type IN
                   ('email','sms','voice','slack','teams','webhook','pagerduty','push')),
    display_name text NOT NULL,
    -- Webhook URLs and API tokens are secrets. Same rule as cloud credentials:
    -- config holds a reference, secret_ref resolves at send time.
    config       jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_ref   text,
    enabled      boolean NOT NULL DEFAULT true,
    verified_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

-- Routing policy: who gets told, through what, after how long, and what happens
-- when nobody responds.
CREATE TABLE notification_profiles (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name       text NOT NULL,
    -- Wait this many confirmed poll failures before notifying anyone.
    notification_delay integer NOT NULL DEFAULT 1 CHECK (notification_delay >= 0),
    -- NULL business_hours_id means "always", matching the -1 sentinel the
    -- evaluated product used.
    business_hours_id  uuid REFERENCES business_hours(id) ON DELETE SET NULL,
    notify_outside_business_hours boolean NOT NULL DEFAULT true,
    -- [{"severity":"down","channels":["<uuid>"],"user_groups":["<uuid>"]}]
    alert_rules        jsonb NOT NULL DEFAULT '[]'::jsonb,
    -- [{"level":1,"after_minutes":15,"channels":[...],"user_groups":[...]}]
    -- The evaluated account had this empty, so a real outage would have
    -- notified nobody beyond the first attempt.
    escalation_levels  jsonb NOT NULL DEFAULT '[]'::jsonb,
    -- Re-notify while an alert stays open, in minutes. 0 disables.
    persistent_alert_interval integer NOT NULL DEFAULT 0,
    notify_on_recovery boolean NOT NULL DEFAULT true,
    rca_needed         boolean NOT NULL DEFAULT true,
    is_default         boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);
CREATE UNIQUE INDEX notif_default_uniq
    ON notification_profiles (tenant_id) WHERE is_default;

ALTER TABLE resources
    ADD CONSTRAINT resources_threshold_fk FOREIGN KEY (threshold_profile_id)
        REFERENCES threshold_profiles(id) ON DELETE SET NULL,
    ADD CONSTRAINT resources_notif_fk FOREIGN KEY (notification_profile_id)
        REFERENCES notification_profiles(id) ON DELETE SET NULL;
ALTER TABLE resource_groups
    ADD CONSTRAINT rg_threshold_fk FOREIGN KEY (threshold_profile_id)
        REFERENCES threshold_profiles(id) ON DELETE SET NULL,
    ADD CONSTRAINT rg_notif_fk FOREIGN KEY (notification_profile_id)
        REFERENCES notification_profiles(id) ON DELETE SET NULL;

-- ===========================================================================
-- 6. ON-CALL
-- ===========================================================================

CREATE TABLE oncall_schedules (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name  text NOT NULL,
    timezone      text NOT NULL DEFAULT 'Asia/Kolkata',
    rotation_type text NOT NULL DEFAULT 'weekly'
                  CHECK (rotation_type IN ('daily','weekly','custom')),
    handoff_time  time NOT NULL DEFAULT '09:00',
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE oncall_shifts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    schedule_id uuid NOT NULL REFERENCES oncall_schedules(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    is_override boolean NOT NULL DEFAULT false,
    CHECK (ends_at > starts_at)
);
CREATE INDEX oncall_shifts_lookup_idx ON oncall_shifts (schedule_id, starts_at, ends_at);

-- ===========================================================================
-- 7. ALERTS, OUTAGES, MAINTENANCE
-- ===========================================================================

CREATE TABLE maintenance_windows (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name    text NOT NULL,
    -- {"resource_ids":[...], "group_ids":[...], "tag_ids":[...]}
    scope           jsonb NOT NULL DEFAULT '{}'::jsonb,
    starts_at       timestamptz NOT NULL,
    ends_at         timestamptz NOT NULL,
    -- NULL = one-off; else RRULE-style recurrence
    recurrence      text,
    suppress_alerts boolean NOT NULL DEFAULT true,
    -- When true, downtime in this window is excluded from SLA calculations.
    exclude_from_sla boolean NOT NULL DEFAULT true,
    created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX maintenance_active_idx ON maintenance_windows (tenant_id, starts_at, ends_at);

-- One row per alert condition per resource. `dedup_key` is what stops a
-- flapping resource from creating thousands of rows: the alerter upserts on it.
CREATE TABLE alerts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_id     uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    dedup_key       text NOT NULL,      -- '<resource_id>:availability' | '<resource_id>:cpu_utilization'
    severity        text NOT NULL CHECK (severity IN ('down','critical','trouble','info')),
    state           text NOT NULL DEFAULT 'open'
                    CHECK (state IN ('open','acknowledged','resolved','suppressed')),
    metric_key      text,
    observed_value  double precision,
    threshold_value double precision,
    message         text NOT NULL,
    -- Consecutive confirming polls seen so far, compared against polls_check.
    poll_count      integer NOT NULL DEFAULT 1,
    opened_at       timestamptz NOT NULL DEFAULT now(),
    notified_at     timestamptz,
    escalation_level integer NOT NULL DEFAULT 0,
    acknowledged_at timestamptz,
    acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
    resolved_at     timestamptz,
    suppressed_by_maintenance uuid REFERENCES maintenance_windows(id) ON DELETE SET NULL,
    rca             jsonb,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
-- At most one live alert per condition per resource.
CREATE UNIQUE INDEX alerts_live_uniq ON alerts (tenant_id, dedup_key)
    WHERE state IN ('open','acknowledged');
CREATE INDEX alerts_open_idx     ON alerts (tenant_id, severity, opened_at DESC)
    WHERE state IN ('open','acknowledged');
CREATE INDEX alerts_resource_idx ON alerts (resource_id, opened_at DESC);

-- Delivery ledger. Separate from `alerts` so a channel outage is retried
-- without touching alert state, and so "did it actually reach anyone" is answerable.
CREATE TABLE alert_notifications (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    alert_id    uuid NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    channel_id  uuid REFERENCES notification_channels(id) ON DELETE SET NULL,
    recipient   text,
    level       integer NOT NULL DEFAULT 0,
    state       text NOT NULL DEFAULT 'pending'
                CHECK (state IN ('pending','sent','failed','skipped')),
    attempts    integer NOT NULL DEFAULT 0,
    last_error  text,
    sent_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_notif_pending_idx ON alert_notifications (state, created_at)
    WHERE state = 'pending';

-- Closed availability intervals, written when a down alert resolves.
-- Reports read from here, never from raw poll history.
CREATE TABLE outages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_id   uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    alert_id      uuid REFERENCES alerts(id) ON DELETE SET NULL,
    started_at    timestamptz NOT NULL,
    ended_at      timestamptz,
    duration_sec  integer,
    severity      text NOT NULL CHECK (severity IN ('down','critical','trouble')),
    classified_as text NOT NULL DEFAULT 'outage'
                  CHECK (classified_as IN ('outage','maintenance','false_positive')),
    root_cause    text,
    comment       text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outages_resource_time_idx ON outages (resource_id, started_at DESC);
CREATE INDEX outages_open_idx ON outages (tenant_id) WHERE ended_at IS NULL;

-- Daily availability rollup. Keeps SLA and availability reports off the
-- time-series store: 2000 resources x 365 days is 730k rows a year, trivial
-- for PostgreSQL, whereas recomputing from samples is not.
CREATE TABLE availability_daily (
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_id      uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    day              date NOT NULL,
    up_sec           integer NOT NULL DEFAULT 0,
    down_sec         integer NOT NULL DEFAULT 0,
    trouble_sec      integer NOT NULL DEFAULT 0,
    maintenance_sec  integer NOT NULL DEFAULT 0,
    unknown_sec      integer NOT NULL DEFAULT 0,
    availability_pct numeric(6,3),
    outage_count     integer NOT NULL DEFAULT 0,
    mttr_sec         integer,
    PRIMARY KEY (tenant_id, resource_id, day)
);
CREATE INDEX availability_daily_day_idx ON availability_daily (tenant_id, day DESC);

CREATE TABLE sla_definitions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name      text NOT NULL,
    target_pct        numeric(6,3) NOT NULL CHECK (target_pct > 0 AND target_pct <= 100),
    scope             jsonb NOT NULL DEFAULT '{}'::jsonb,
    business_hours_id uuid REFERENCES business_hours(id) ON DELETE SET NULL,
    period            text NOT NULL DEFAULT 'monthly'
                      CHECK (period IN ('daily','weekly','monthly','quarterly')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

-- ===========================================================================
-- 8. COLLECTOR SCHEDULING
-- ===========================================================================

-- Durable record of collector work. Redis carries the live queue; this table is
-- the schedule of record and the history, so a Redis restart loses nothing and
-- "why did this resource stop updating" is answerable.
CREATE TABLE collector_jobs (
    id               bigserial PRIMARY KEY,
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cloud_account_id uuid REFERENCES cloud_accounts(id) ON DELETE CASCADE,
    job_type         text NOT NULL CHECK (job_type IN ('discovery','metrics','availability','cost')),
    resource_type    text REFERENCES resource_types(code),
    region           text,
    scheduled_at     timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    state            text NOT NULL DEFAULT 'queued'
                     CHECK (state IN ('queued','running','ok','failed','skipped')),
    attempt          integer NOT NULL DEFAULT 0,
    worker_id        text,
    items_processed  integer,
    -- Cloud metric APIs are rate limited; this is the number the scheduler
    -- backs off on. It is the real ceiling on how many resources one box polls.
    api_calls        integer,
    error            text
);
CREATE INDEX collector_jobs_due_idx ON collector_jobs (state, scheduled_at)
    WHERE state = 'queued';
CREATE INDEX collector_jobs_hist_idx ON collector_jobs (cloud_account_id, finished_at DESC);

COMMIT;

-- NimbusEye — Nimbus FinOps schema (cost and budget module)
-- Cost data is a fundamentally different pipeline from metrics: it arrives as
-- daily batch files from billing exports, is wide, and is re-stated by the
-- provider for days after the fact. It therefore gets its own tables, its own
-- ingest jobs, and idempotent upserts keyed on the billing period.

BEGIN;

-- Where the billing export lives. One row per cloud account that has cost
-- reporting enabled; not every monitored account will.
--   aws   : Cost and Usage Report (CUR) in S3
--   azure : Cost Management scheduled export to a storage container
--   gcp   : BigQuery billing export dataset
--   oci   : Cost and Usage Reports in Object Storage
CREATE TABLE cost_sources (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cloud_account_id uuid NOT NULL REFERENCES cloud_accounts(id) ON DELETE CASCADE,
    provider         text NOT NULL CHECK (provider IN ('oci','aws','azure','gcp')),
    -- {"bucket":"...","prefix":"...","dataset":"...","namespace":"..."}
    location         jsonb NOT NULL DEFAULT '{}'::jsonb,
    credentials_ref  text,
    enabled          boolean NOT NULL DEFAULT true,
    -- Providers restate recent cost. Re-importing the trailing window keeps
    -- figures correct instead of frozen at first read.
    restatement_days integer NOT NULL DEFAULT 7,
    -- Azure can report list price rather than actual negotiated cost. Making
    -- this explicit avoids silently reporting the wrong number.
    use_list_price   boolean NOT NULL DEFAULT false,
    last_import_at   timestamptz,
    last_import_state text CHECK (last_import_state IN ('never','running','ok','partial','failed'))
                     DEFAULT 'never',
    last_error       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, cloud_account_id)
);

CREATE TABLE cost_import_runs (
    id              bigserial PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cost_source_id  uuid NOT NULL REFERENCES cost_sources(id) ON DELETE CASCADE,
    billing_period  date NOT NULL,          -- first day of the month
    covers_from     date NOT NULL,
    covers_to       date NOT NULL,
    state           text NOT NULL DEFAULT 'running'
                    CHECK (state IN ('running','ok','failed')),
    rows_loaded     bigint NOT NULL DEFAULT 0,
    bytes_read      bigint,
    -- Detects an unchanged export so a re-run costs nothing.
    source_checksum text,
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    error           text
);
CREATE INDEX cost_import_runs_src_idx ON cost_import_runs (cost_source_id, billing_period DESC);

-- Daily granularity, not hourly. Hourly across 4 clouds and 2000 resources is
-- roughly 50x the rows for detail nobody acts on; daily keeps a year of history
-- inside a single Postgres instance. Range-partitioned by month so old periods
-- can be detached rather than deleted.
CREATE TABLE cost_line_items (
    tenant_id         uuid NOT NULL,
    cost_source_id    uuid NOT NULL,
    cloud_account_id  uuid NOT NULL,
    provider          text NOT NULL,
    usage_date        date NOT NULL,
    -- Native id from the bill. Often present when the monitored resource is
    -- not, and vice versa, so this is a soft link resolved after import.
    native_resource_id text,
    resource_id       uuid,             -- resolved link into resources, nullable
    service           text NOT NULL,    -- 'Compute', 'Object Storage', 'AmazonRDS'
    sku               text,
    usage_type        text,
    region            text,
    charge_type       text,             -- Usage | Tax | Credit | Refund | Commitment
    usage_qty         numeric(24,6),
    usage_unit        text,
    unit_price        numeric(24,10),
    -- The number shown in the UI. For AWS this is amortised cost, for Azure it
    -- honours use_list_price, for GCP it is cost minus credits.
    cost              numeric(24,6) NOT NULL DEFAULT 0,
    -- Kept separately so effective-savings reporting is possible later.
    list_cost         numeric(24,6),
    credit            numeric(24,6) NOT NULL DEFAULT 0,
    currency          char(3) NOT NULL DEFAULT 'INR',
    -- Cost allocation tags copied from the bill.
    tags              jsonb NOT NULL DEFAULT '{}'::jsonb,
    imported_run_id   bigint,
    -- Identity of a bill line, computed by the importer as an md5 over
    -- (cost_source_id, provider, service, native_resource_id, sku, usage_type,
    --  charge_type). A single opaque column is used rather than a wide natural
    -- key because PostgreSQL does not permit expressions such as COALESCE()
    -- inside a PRIMARY KEY, and several of those fields are nullable.
    -- This is what makes re-importing a restated billing period idempotent:
    -- the importer upserts ON CONFLICT (tenant_id, usage_date, line_key).
    line_key          text NOT NULL,
    -- usage_date must be part of the key because it is the partition key.
    PRIMARY KEY (tenant_id, usage_date, line_key)
) PARTITION BY RANGE (usage_date);

CREATE INDEX cost_li_tenant_date_idx  ON cost_line_items (tenant_id, usage_date DESC);
CREATE INDEX cost_li_service_idx      ON cost_line_items (tenant_id, provider, service, usage_date DESC);
CREATE INDEX cost_li_resource_idx     ON cost_line_items (resource_id, usage_date DESC)
    WHERE resource_id IS NOT NULL;
CREATE INDEX cost_li_tags_idx         ON cost_line_items USING gin (tags);

-- Partitions are created ahead of time by a maintenance job. Two are seeded so
-- a fresh install can ingest immediately.
CREATE TABLE cost_line_items_2026_09 PARTITION OF cost_line_items
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE cost_line_items_2026_10 PARTITION OF cost_line_items
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
-- Catch-all so an out-of-range date fails the import loudly instead of silently.
CREATE TABLE cost_line_items_default PARTITION OF cost_line_items DEFAULT;

-- Pre-aggregated daily totals. The dashboard reads this, never the line items.
CREATE MATERIALIZED VIEW cost_daily_summary AS
SELECT tenant_id, cloud_account_id, provider, usage_date, service, region,
       sum(cost)   AS cost,
       sum(credit) AS credit,
       currency
FROM cost_line_items
WHERE charge_type IS NULL OR charge_type NOT IN ('Tax','Refund')
GROUP BY tenant_id, cloud_account_id, provider, usage_date, service, region, currency
WITH NO DATA;
CREATE UNIQUE INDEX cost_daily_summary_uniq ON cost_daily_summary
    (tenant_id, cloud_account_id, provider, usage_date, service,
     COALESCE(region,''), currency);

-- Showback/chargeback grouping. match_rules assigns spend by tag, account or
-- resource group, e.g. {"tags":{"env":"prod"}} or {"accounts":["..."]}.
CREATE TABLE cost_centers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name text NOT NULL,
    parent_id    uuid REFERENCES cost_centers(id) ON DELETE CASCADE,
    owner_email  citext,
    match_rules  jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Lower number wins when a line item matches several cost centres.
    priority     integer NOT NULL DEFAULT 100,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE budgets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name    text NOT NULL,
    -- {"cost_center_ids":[...], "accounts":[...], "services":[...], "tags":{...}}
    scope           jsonb NOT NULL DEFAULT '{}'::jsonb,
    period          text NOT NULL DEFAULT 'monthly'
                    CHECK (period IN ('monthly','quarterly','annual')),
    amount          numeric(18,2) NOT NULL CHECK (amount > 0),
    currency        char(3) NOT NULL DEFAULT 'INR',
    -- Percent-of-budget trip points, e.g. {50,80,100}. Forecast breaches fire
    -- on projected month-end spend, not only on actual.
    alert_percents  integer[] NOT NULL DEFAULT '{80,100}',
    alert_on_forecast boolean NOT NULL DEFAULT true,
    notification_profile_id uuid REFERENCES notification_profiles(id) ON DELETE SET NULL,
    enabled         boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, display_name)
);

CREATE TABLE budget_evaluations (
    id             bigserial PRIMARY KEY,
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    budget_id      uuid NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    period_start   date NOT NULL,
    actual_cost    numeric(18,2) NOT NULL DEFAULT 0,
    forecast_cost  numeric(18,2),
    pct_consumed   numeric(6,2),
    breached_pct   integer,
    evaluated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (budget_id, period_start, breached_pct)
);

COMMIT;

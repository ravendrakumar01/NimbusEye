-- NimbusEye — support for user-created (synthetic) monitors.
--
-- Until now every resource arrived from a cloud collector, so identity was
-- (tenant_id, cloud_account_id, native_id). A synthetic monitor — a website, a
-- port, a DNS record — belongs to no cloud account, so cloud_account_id is NULL.
--
-- That breaks the uniqueness guarantee: PostgreSQL treats NULLs as distinct in a
-- unique constraint, so `UNIQUE (tenant_id, cloud_account_id, native_id)` does
-- not constrain synthetic monitors at all. The same website could be added a
-- hundred times, each creating a separate monitor that alerts separately.
--
-- A partial unique index covering exactly the NULL-account case closes it.

BEGIN;

CREATE UNIQUE INDEX resources_synthetic_uniq
    ON resources (tenant_id, native_id)
    WHERE cloud_account_id IS NULL AND deleted_at IS NULL;

-- Check configuration for user-created monitors, kept beside the resource rather
-- than in a side table: it is small, always read with the resource, and its shape
-- differs per monitor type.
--
--   {"target":"https://example.com","method":"GET","expected_status":[200],
--    "timeout_sec":10,"follow_redirects":true,"match_text":"...",
--    "port":443,"record_type":"A","resolver":"1.1.1.1"}
ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS check_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- How often the prober should run this check. Distinct from the cloud metric
    -- interval: a website can be checked every minute, a cloud API cannot.
    ADD COLUMN IF NOT EXISTS check_interval_sec integer,
    -- Set by the prober so a check that stops running is visible rather than
    -- appearing merely healthy.
    ADD COLUMN IF NOT EXISTS last_check_at timestamptz,
    ADD COLUMN IF NOT EXISTS created_by uuid REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE resources
    ADD CONSTRAINT resources_check_interval_sane
    CHECK (check_interval_sec IS NULL OR check_interval_sec BETWEEN 30 AND 86400);

-- The prober claims work with this: due checks, oldest first.
CREATE INDEX resources_due_check_idx
    ON resources (last_check_at NULLS FIRST)
    WHERE cloud_account_id IS NULL AND deleted_at IS NULL AND NOT suspended;

COMMIT;

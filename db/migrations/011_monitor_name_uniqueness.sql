-- NimbusEye — identify synthetic monitors by name, not by target.
--
-- 009 derived a synthetic monitor's native_id from its type and target, so the
-- partial unique index refused a second monitor on the same URL. That is wrong:
-- several checks against one URL are legitimate and common — one asserting a
-- status code, another asserting page content, a third on a slower interval for
-- a different threshold.
--
-- The constraint people actually expect is on the name: "you already have a
-- monitor called X". Targets may repeat; names may not.
--
-- Existing synthetic monitors keep their derived native_id. It is opaque either
-- way, and rewriting it would orphan their metric history, which is keyed on it.

BEGIN;

DROP INDEX IF EXISTS resources_synthetic_uniq;

CREATE UNIQUE INDEX resources_synthetic_name_uniq
    ON resources (tenant_id, lower(display_name))
    WHERE cloud_account_id IS NULL AND deleted_at IS NULL;

COMMIT;

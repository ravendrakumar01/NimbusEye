-- NimbusEye — persist what discovery found but does not monitor.
--
-- Every collection run already works out which OCI resource types it saw and could
-- not map to a monitor, and which it deliberately ignored. Until now that only
-- reached the log, so the one place it mattered — "what does this tenancy contain
-- that we are not watching" — could not be answered from the console.
--
-- On the live tenancy the figures are stark: about 15,400 objects found, 149
-- monitored, 15,180 ignored as noise (container images, backups), and 11 types
-- unmapped covering API gateways, bastions, DRGs, NAT and service gateways. Some
-- of those are worth monitoring and nobody could see that they existed.
--
-- Stored as a snapshot on the account rather than as history: the question is
-- always about the current estate, and a per-run archive of 15,000-object counts
-- would grow for no reader.

BEGIN;

ALTER TABLE cloud_accounts
    ADD COLUMN IF NOT EXISTS discovered_total integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS mapped_total     integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS ignored_total    integer NOT NULL DEFAULT 0,
    -- {"ApiGateway": 4, "Bastion": 4, ...}
    ADD COLUMN IF NOT EXISTS unmapped_types   jsonb   NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN cloud_accounts.unmapped_types IS
    'Resource types the last discovery saw and could not map to a monitor type. '
    'Counts, keyed by the provider''s own type name.';

COMMIT;

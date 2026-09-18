-- NimbusEye — stop cloud resources flapping between up and unknown.
--
-- A resource is promoted to `up` when it returns a metric, because that is the only
-- real availability signal a cloud API offers — the control plane reporting that an
-- instance exists says nothing about whether it is working.
--
-- But OCI Monitoring does not return a datapoint for every resource on every read.
-- Across four consecutive collection runs against one tenancy the confirmed count
-- was 43, 51, 59 and 65 out of the same 148 resources. Nothing changed in the
-- tenancy; the metric window simply had data sometimes and not others.
--
-- Treating each run as authoritative therefore made resources flip up → unknown →
-- up every fifteen minutes, which resets status_since and means "up for three
-- hours" can never be displayed. It also makes the dashboard counts move for no
-- reason, which trains people to ignore them.
--
-- confirmed_at records when a metric last proved the resource alive. A run that
-- returns nothing no longer demotes a resource that was confirmed recently; it only
-- does so once the silence has lasted long enough to mean something.

BEGIN;

ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS confirmed_at timestamptz;

COMMENT ON COLUMN resources.confirmed_at IS
    'When a collected metric last proved this resource alive. Used to hold an "up" '
    'status through the gaps in cloud metric availability rather than flapping.';

-- Resources currently up were confirmed by the run that set them up, so seed the
-- column rather than leaving every one of them eligible for immediate demotion on
-- the next pass.
UPDATE resources
   SET confirmed_at = COALESCE(last_polled_at, status_since, now())
 WHERE status = 'up' AND confirmed_at IS NULL;

-- The prober's own checks are direct measurements and never ambiguous, so they are
-- confirmed the moment they succeed.
UPDATE resources
   SET confirmed_at = COALESCE(last_check_at, now())
 WHERE cloud_account_id IS NULL AND status = 'up' AND confirmed_at IS NULL;

CREATE INDEX resources_confirmed_idx ON resources (confirmed_at)
    WHERE deleted_at IS NULL;

COMMIT;

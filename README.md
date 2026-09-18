# NimbusEye

Multi-cloud monitoring for OCI, AWS, Azure, GCP and Kubernetes, with a cost
module (**Nimbus FinOps**) built on billing exports.

Target scale is ~2000 monitored resources on a single 8 vCPU / 32 GB server.

## Current state

| Component | Status |
| --- | --- |
| PostgreSQL schema (36 tables, RLS, auth) | done, applied and verified on PostgreSQL 16 |
| Resource type catalog (46 types, 86 metrics) | done |
| Go API — resources, alarms, status, metrics | done, mock mode only |
| UI — Home, Alarms, Cloud, Kubernetes, Web | done |
| UI — resource detail with charts | done |
| UI — Admin > Cloud Accounts | done |
| UI — FinOps, Reports | placeholders |
| OCI collector (discovery + metrics) | code complete, untested against a real tenancy |
| AWS / Azure / GCP collectors | not started |
| PostgreSQL-backed store | done, verified end to end |
| Alert evaluator | done, verified |
| Notification delivery | not started — intents are recorded, nothing is sent |
| Deployment (systemd, nginx, TLS) | not started |

The API runs against PostgreSQL by default, and in **mock mode** with `--mock`,
which generates a deterministic 2000-resource estate in memory and needs no
database and no cloud credentials. Mock mode is for UI work; both backends
implement the same `store.Store` interface, asserted at compile time, so a query
cannot mean one thing in mock mode and another in production.

## Database

PostgreSQL is provisioned by:

```bash
sudo bash deploy/setup-postgres.sh   # once
go run ./cmd/api --migrate           # applies db/migrations in order
```

The script creates two roles on purpose. `nimbuseye_migrator` owns the schema and
is used only by migrations; `nimbuseye_app` is the application role and is neither
owner nor superuser, and is explicitly `NOBYPASSRLS`. This matters because
PostgreSQL exempts both superusers and table owners from row-level security — an
application connecting as the owner would have tenant isolation silently
disabled.

Generated DSNs land in `~/.nimbuseye/db.env` (mode 600) and are never committed.

## Run locally

Requires Go 1.27+ and Node 20+.

```bash
# terminal 1 — API on :8080
set -a; . ~/.nimbuseye/db.env; set +a   # PostgreSQL
go run ./cmd/api
# ...or with generated data and no database:
go run ./cmd/api --mock

# terminal 2 — UI on :5173, proxies /api to the Go server
cd web && npm install && npm run dev
```

To let the collector write to the API, give both a shared token:

```bash
head -c 48 /dev/urandom | base64 | tr -d '\n' > ~/.nimbuseye/ingest.token
chmod 600 ~/.nimbuseye/ingest.token
go run ./cmd/api --ingest-token-file ~/.nimbuseye/ingest.token
```

Without that flag the ingest endpoints are not registered at all, so a default
install cannot be written to by anyone who can reach the port.

Open <http://localhost:5173>. A "Demo data" badge appears in the top bar so a
generated estate is never mistaken for real infrastructure.

Useful flags:

```bash
go run ./cmd/api --mock --mock-resources 5000   # load-test the UI
go run ./cmd/api --mock --mock-seed 42          # a different estate
```

The API binds to `127.0.0.1` by default. It has **no authentication yet**, so it
must not be bound to a public interface until the auth layer is built.

## Layout

```
cmd/api            HTTP API entry point
cmd/collector      cloud discovery and metric collection   (not started)
cmd/alerter        threshold evaluation, outages, availability rollup
cmd/finops         billing export ingestion                 (not started)
internal/catalog   resource type catalog, embedded from catalog.json
internal/demo      deterministic in-memory estate for mock mode
internal/httpapi   routes, handlers, middleware
internal/model     domain types shared across binaries
db/migrations      PostgreSQL schema
web                React + TypeScript + Tailwind frontend
deploy             systemd units and nginx config           (not started)
```

## Design decisions

**Thresholds bind to a resource type, not to a resource.** One profile governs
every OCI compute instance. Per-resource thresholds become unmanageable well
before 2000 resources.

**Every alert rule carries a `polls_check` count.** Cloud metrics arrive late and
occasionally out of order, so alerting on a single sample produces constant false
positives. The evaluator requires that many *consecutive* confirming samples
before it opens anything.

**A running control-plane state is not an availability check.** The OCI collector
maps lifecycle `RUNNING` to `unknown`, not `up`. Reporting green because the
provider says the instance exists is how a monitoring tool ends up showing healthy
for a host that stopped responding an hour ago.

**Collector health is a first-class field.** `cloud_accounts.last_discovery_status`
and `consecutive_failures` are surfaced on the dashboard. A cloud integration that
has silently stopped discovering makes a monitoring tool actively misleading.

**Tenant isolation is enforced in the database.** Row-level security means a
handler that forgets its `tenant_id` filter returns nothing rather than leaking
across tenants. Two narrow `SECURITY DEFINER` functions are the only pre-auth read
path, because RLS otherwise makes login impossible.

**Metrics do not live in PostgreSQL.** At 2000 resources and 5-minute polling the
estate produces roughly 8.6M samples a day. VictoriaMetrics holds the series;
PostgreSQL holds inventory, configuration, alert state and daily rollups.

**Cost is a separate pipeline at daily granularity.** Billing exports are batch,
wide, and restated by the provider for days afterwards, so cost ingestion is
idempotent on `(tenant_id, usage_date, line_key)` and re-reads a trailing window.

## Credentials

Cloud credentials are never stored in the database or the repository.
`cloud_accounts.credentials_ref` holds a path or key id, and the collector
resolves the secret at runtime. All cloud access is intended to be **read-only**.

## Not built

The navigation shows only what works. The reference console has more, and the gaps
are recorded here rather than greyed out in the interface: a console full of dead
entries reads as broken, and this one is in daily use.

### Absent sections

| Section | Why |
|---|---|
| Server | Needs a per-OS agent. A separate product, not a feature |
| APM | Needs a per-language agent, same |
| Nimbus FinOps | Database schema exists; the billing ingest does not |

Kubernetes clusters are discovered and alerted on through OCI's own metrics —
unschedulable pods, node conditions, API server load — but there is no in-cluster
agent, so no pod or workload detail.

### Removed rows, by section

**Home** — Help Assistant (needs a documentation corpus), Dashboards (no builder),
Capacity Planning (needs months of history), Zia Anomaly Dashboard (no model),
Schedule IT Automation and IT Automation Logs (no runner), Log Report (no log
ingestion).

**Web** — the browser-based checks: Web Transaction, SaaS Synthetics, Webpage
Speed, Synthetic Mobile App. These need a headless browser fleet, which is
infrastructure rather than code. Also SOAP, gRPC, REST API Transaction, File
Upload, Application Cluster, WebSocket, UDP Port, Mail Delivery and Heartbeat —
each is a prober check that could be written; none is written.

**Admin** — Smart Groups (dynamic membership by rule; the `match_rules` column
exists and nothing fills it), Import and Export Monitors, Configuration Rules,
User Groups, On-Call Schedules, and the empty Automations, Server Monitor and
AppLogs group headers.

**Reports** — Forecast (needs history to extrapolate from), Custom Reports (a
report builder), Schedule Reports (needs a scheduler).

### Elsewhere

- **Alert delivery.** The evaluator decides who should be told and records the
  intent in `alert_notifications`; nothing sends yet. SMTP is configured, so this
  is the shortest path from "detects problems" to "tells somebody". The Alert Logs
  page shows exactly what is currently not being delivered.
- **AWS, Azure and GCP collectors.** The OCI one establishes the pattern.
- **VictoriaMetrics.** `metric_samples` lives in PostgreSQL, day-partitioned, and
  is explicitly interim.
- **MFA**, and a session cleanup job.

### Structural, not merely unwritten

These need infrastructure or agents rather than more code, and the distinction
matters: only one kind belongs on a roadmap.

- **Synthetic transactions** need a headless browser fleet.
- **Global check locations** would mean probe servers in many regions. The
  reference product has over a hundred; a handful is the sane target here.
- **Multi-tenancy** is fully modelled and enforced in the database, but the store
  binds one tenant at startup. Selling to a second customer needs per-request
  tenant resolution first.

## Licence

Proprietary. Not for redistribution.

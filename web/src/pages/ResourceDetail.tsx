/**
 * Resource detail — the monitor view.
 *
 * Follows the reference console's monitor page: a breadcrumb title with the
 * monitor's current state and actions, a tab strip, then a summary built from
 * availability counters, one chart per metric with its thresholds drawn in, and
 * the alarm and outage history for that resource.
 *
 * The charts read their threshold lines from the same catalog definition the
 * alert evaluator uses, so what the chart draws and what would actually fire are
 * guaranteed to agree.
 */

import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ChevronRight, Pause, PlayCircle, RefreshCw, Settings2, Trash2, X } from "lucide-react";
import { api } from "../lib/api";
import { useAsync, usePolling } from "../lib/hooks";
import {
  CATEGORY_LABEL,
  PROVIDER_LABEL,
  absolute,
  availabilityTone,
  cx,
  duration,
  metricValue,
  num,
  pct,
  since,
} from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  InfoBanner,
  PageHeader,
  SeverityBadge,
  Spinner,
  StatusBadge,
  TabStrip,
} from "../components/ui";
import { MetricChart } from "../components/Chart";
import { Modal } from "./AdminCloudAccounts";

const RANGES = [
  { value: "1h", label: "1 Hr", hours: 1 },
  { value: "6h", label: "6 Hrs", hours: 6 },
  { value: "24h", label: "24 Hrs", hours: 24 },
  { value: "7d", label: "7 Days", hours: 24 * 7 },
] as const;

type Tab = "summary" | "outages" | "alarms" | "inventory" | "configuration";

export function ResourceDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") ?? "summary") as Tab;
  const [range, setRange] = useState<(typeof RANGES)[number]["value"]>("24h");
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<{ kind: "ok" | "err"; text: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const detail = useAsync(() => api.resource(id), [id]);
  const outages = useAsync(() => api.outages({ resource_id: id, page_size: 50 }), [id]);

  const hours = RANGES.find((r) => r.value === range)?.hours ?? 24;
  const metrics = useAsync(() => {
    const to = new Date();
    const from = new Date(to.getTime() - hours * 3600_000);
    return api.metrics(id, {
      from: from.toISOString(),
      to: to.toISOString(),
      points: hours <= 6 ? 120 : 288,
    });
  }, [id, hours]);

  usePolling(() => {
    detail.reload();
    metrics.reload();
  }, 60_000);

  async function act(label: string, fn: () => Promise<unknown>, message: string) {
    setBusy(label);
    setNotice(null);
    try {
      await fn();
      setNotice({ kind: "ok", text: message });
      detail.reload();
      outages.reload();
    } catch (e) {
      setNotice({ kind: "err", text: e instanceof Error ? e.message : "Action failed" });
    } finally {
      setBusy(null);
    }
  }

  if (detail.initialLoading) return <Spinner label="Loading monitor" />;
  if (detail.error) return <ErrorState error={detail.error} onRetry={detail.reload} />;
  if (!detail.data) return null;

  const { resource: r, type, alarms } = detail.data;
  // Cloud-discovered resources have an owning account; their lifecycle belongs to
  // that account, not to a button here.
  const isSynthetic = !r.cloud_account_id;
  const openAlarms = alarms.filter((a) => a.state === "open" || a.state === "acknowledged");
  const ongoing = outages.data?.items.filter((o) => o.ended_at === null) ?? [];

  // Downtime and MTTR over the outage history we hold, which is the same source
  // the reports will use — so the numbers here will not disagree with them.
  const closed = outages.data?.items.filter((o) => o.ended_at !== null) ?? [];
  const totalDown = closed.reduce((a, o) => a + o.duration_sec, 0);
  const mttr = closed.length ? Math.round(totalDown / closed.length) : null;

  function setTab(t: Tab) {
    const next = new URLSearchParams(params);
    if (t === "summary") next.delete("tab");
    else next.set("tab", t);
    setParams(next, { replace: true });
  }

  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-1.5">
            <Link
              to={r.provider === "synthetic" ? "/web" : `/cloud?provider=${r.provider}`}
              className="text-brand-500 hover:underline"
            >
              {PROVIDER_LABEL[r.provider] ?? r.provider}
            </Link>
            <ChevronRight className="size-3.5 text-slate-400" aria-hidden="true" />
            <Link
              to={
                r.provider === "synthetic"
                  ? "/web"
                  : `/cloud?provider=${r.provider}&type=${r.resource_type}`
              }
              className="text-brand-500 hover:underline"
            >
              {r.type_name}
            </Link>
            <ChevronRight className="size-3.5 text-slate-400" aria-hidden="true" />
            <span className="text-slate-800">{r.display_name}</span>
          </span>
        }
        actions={
          <>
            <StatusBadge status={r.status} />
            <Button size="xs" onClick={() => detail.reload()} disabled={detail.loading}>
              <RefreshCw
                className={cx("size-3.5", detail.loading && "animate-spin")}
                aria-hidden="true"
              />
              Refresh
            </Button>
            {isSynthetic ? (
              <>
                <Button
                  size="xs"
                  disabled={busy !== null}
                  onClick={() =>
                    r.suspended
                      ? act("activate", () => api.activateMonitor(r.id), "Monitor activated. It will be checked on the next pass.")
                      : act("suspend", () => api.suspendMonitor(r.id), "Monitor suspended. It will not be checked or alerted on.")
                  }
                >
                  {r.suspended ? (
                    <>
                      <PlayCircle className="size-3.5" aria-hidden="true" />
                      {busy === "activate" ? "Working…" : "Activate"}
                    </>
                  ) : (
                    <>
                      <Pause className="size-3.5" aria-hidden="true" />
                      {busy === "suspend" ? "Working…" : "Suspend"}
                    </>
                  )}
                </Button>
                <Button size="xs" variant="danger" disabled={busy !== null} onClick={() => setConfirmDelete(true)}>
                  <Trash2 className="size-3.5" aria-hidden="true" />
                  Delete
                </Button>
              </>
            ) : (
              <Link to={`/admin/cloud-accounts/${r.cloud_account_id}`}>
                <Button size="xs">
                  <Settings2 className="size-3.5" aria-hidden="true" />
                  Manage Account
                </Button>
              </Link>
            )}
          </>
        }
      />

      <div className="border-b border-slate-200 px-5">
        <TabStrip
          label="Monitor sections"
          value={tab}
          onChange={setTab}
          tabs={[
            { value: "summary", label: "Summary" },
            { value: "alarms", label: "Alarms", count: openAlarms.length },
            { value: "outages", label: "Outages", count: outages.data?.total ?? 0 },
            { value: "inventory", label: "Inventory" },
            { value: "configuration", label: "Configuration" },
          ]}
        />
      </div>

      {notice && (
        <div
          role="status"
          className={cx(
            "mx-5 mt-4 flex items-center gap-2 rounded border px-3 py-2 text-[13px]",
            notice.kind === "ok"
              ? "border-st-up/30 bg-st-up-bg text-slate-700"
              : "border-st-down/30 bg-st-down-bg text-slate-700",
          )}
        >
          <span className="flex-1">{notice.text}</span>
          <button type="button" onClick={() => setNotice(null)} aria-label="Dismiss">
            <X className="size-3.5 text-slate-400" aria-hidden="true" />
          </button>
        </div>
      )}

      {confirmDelete && (
        <Modal title="Delete monitor" onClose={() => setConfirmDelete(false)}>
          <p className="text-[13px] text-slate-700">
            Delete <strong>{r.display_name}</strong>? It will stop being checked immediately.
          </p>
          <p className="mt-2 text-[13px] text-slate-500">
            Its outage and availability history is retained for reports covering the period it
            existed.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button
              variant="danger"
              onClick={async () => {
                setConfirmDelete(false);
                await act("delete", () => api.deleteMonitor(r.id), "Monitor deleted.");
                navigate("/");
              }}
            >
              Delete monitor
            </Button>
          </div>
        </Modal>
      )}

      <div className="px-5 py-4">
        {tab === "summary" && (
          <div className="space-y-4">
            {ongoing.length > 0 && (
              <InfoBanner tone="warn">
                This monitor has been <strong>{r.status}</strong> for{" "}
                <strong>{since(ongoing[0]!.started_at)}</strong>, since{" "}
                {absolute(ongoing[0]!.started_at)}.
              </InfoBanner>
            )}
            {r.suspended && (
              <InfoBanner>
                This monitor is suspended. It is not being polled and will not raise alarms.
              </InfoBanner>
            )}

            {/* Counters */}
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {[
                {
                  label: "Availability 24h",
                  value: r.availability_24h > 0 ? pct(r.availability_24h) : "—",
                  tone: availabilityTone(r.availability_24h),
                },
                {
                  label: "Open Alarms",
                  value: num(openAlarms.length),
                  tone: openAlarms.length ? "text-st-down" : "text-slate-800",
                },
                {
                  label: "Outages recorded",
                  value: num(outages.data?.total ?? 0),
                  tone: "text-slate-800",
                },
                {
                  label: "Mean time to recovery",
                  value: mttr === null ? "—" : duration(mttr),
                  tone: "text-slate-800",
                },
              ].map((c) => (
                <Card key={c.label} className="px-4 py-3">
                  <div className={cx("num text-xl font-medium", c.tone)}>{c.value}</div>
                  <div className="mt-0.5 text-[11px] text-slate-500">{c.label}</div>
                </Card>
              ))}
            </div>

            {/* Charts */}
            <div className="flex items-center justify-between">
              <h2 className="text-[13px] font-medium text-slate-700">Performance</h2>
              <div className="inline-flex overflow-hidden rounded border border-slate-300">
                {RANGES.map((opt, i) => (
                  <button
                    key={opt.value}
                    type="button"
                    onClick={() => setRange(opt.value)}
                    aria-pressed={range === opt.value}
                    className={cx(
                      "px-2.5 py-1 text-xs transition",
                      i > 0 && "border-l border-slate-300",
                      range === opt.value
                        ? "bg-slate-100 font-medium text-slate-800"
                        : "bg-white text-slate-600 hover:bg-slate-50",
                    )}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
            </div>

            {!type.supports_metrics || type.metrics.length === 0 ? (
              <Card className="px-4 py-8">
                <EmptyState
                  title="No metrics defined for this type"
                  hint={`${type.display_name} is monitored for availability only.`}
                />
              </Card>
            ) : metrics.initialLoading ? (
              <Spinner label="Loading metrics" />
            ) : metrics.error ? (
              <ErrorState error={metrics.error} onRetry={metrics.reload} />
            ) : (
              <div className="grid gap-3 xl:grid-cols-2">
                {metrics.data?.series.map((s) => {
                  const last = s.samples[s.samples.length - 1]?.v;
                  return (
                    <Card key={s.metric_key} className="px-3 pt-3 pb-1">
                      <div className="flex items-baseline justify-between px-1">
                        <h3 className="text-[13px] font-medium text-slate-700">{s.label}</h3>
                        <span className="num text-[13px] font-medium text-slate-800">
                          {metricValue(last, s.unit)}
                        </span>
                      </div>
                      <MetricChart series={s} className="mt-1" />
                    </Card>
                  );
                })}
              </div>
            )}
          </div>
        )}

        {tab === "alarms" && (
          <Card className="overflow-hidden">
            {alarms.length === 0 ? (
              <EmptyState title="No alarms recorded for this monitor" />
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-[13px] text-slate-600">
                    <th scope="col" className="px-3 py-2.5 font-medium">Severity</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">Condition</th>
                    <th scope="col" className="px-3 py-2.5 text-right font-medium">Observed</th>
                    <th scope="col" className="px-3 py-2.5 text-right font-medium">Threshold</th>
                    <th scope="col" className="px-3 py-2.5 text-right font-medium">Opened</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">State</th>
                  </tr>
                </thead>
                <tbody>
                  {alarms.map((a, i) => (
                    <tr
                      key={a.id}
                      className={cx("border-b border-slate-100 last:border-0", i % 2 === 1 && "bg-slate-50/70")}
                    >
                      <td className="px-3 py-2.5"><SeverityBadge severity={a.severity} /></td>
                      <td className="px-3 py-2.5 text-slate-700">
                        {a.message}
                        <div className="text-[11px] text-slate-500">
                          confirmed over {a.poll_count} poll{a.poll_count === 1 ? "" : "s"}
                        </div>
                      </td>
                      <td className="num px-3 py-2.5 text-right font-medium text-slate-800">
                        {metricValue(a.observed_value, a.unit)}
                      </td>
                      <td className="num px-3 py-2.5 text-right text-slate-500">
                        {metricValue(a.threshold_value, a.unit)}
                      </td>
                      <td className="num px-3 py-2.5 text-right text-slate-600" title={absolute(a.opened_at)}>
                        {since(a.opened_at)} ago
                      </td>
                      <td className="px-3 py-2.5 text-[13px]">
                        {a.state === "resolved" ? (
                          <span className="text-st-up">Resolved</span>
                        ) : a.state === "acknowledged" ? (
                          <span className="text-slate-600">Ack by {a.acknowledged_by}</span>
                        ) : (
                          <span className="font-medium text-st-down">Open</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        )}

        {tab === "outages" && (
          <Card className="overflow-hidden">
            {outages.initialLoading ? (
              <Spinner />
            ) : !outages.data?.items.length ? (
              <EmptyState title="No outages recorded" hint="This monitor has not been down in the retained history." />
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-[13px] text-slate-600">
                    <th scope="col" className="px-3 py-2.5 font-medium">Started</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">Ended</th>
                    <th scope="col" className="px-3 py-2.5 text-right font-medium">Duration</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">Severity</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">Classified as</th>
                    <th scope="col" className="px-3 py-2.5 font-medium">Root cause</th>
                  </tr>
                </thead>
                <tbody>
                  {outages.data.items.map((o, i) => (
                    <tr
                      key={o.id}
                      className={cx("border-b border-slate-100 last:border-0", i % 2 === 1 && "bg-slate-50/70")}
                    >
                      <td className="num px-3 py-2.5 text-slate-700">{absolute(o.started_at)}</td>
                      <td className="num px-3 py-2.5 text-slate-700">
                        {o.ended_at ? absolute(o.ended_at) : <span className="text-st-down">ongoing</span>}
                      </td>
                      <td className="num px-3 py-2.5 text-right text-slate-700">{duration(o.duration_sec)}</td>
                      <td className="px-3 py-2.5"><SeverityBadge severity={o.severity} /></td>
                      <td className="px-3 py-2.5 text-[13px] text-slate-600">
                        {o.classified_as === "maintenance" ? (
                          <span className="text-st-maintenance">Maintenance</span>
                        ) : o.classified_as === "false_positive" ? (
                          <span className="text-slate-500">False positive</span>
                        ) : (
                          "Outage"
                        )}
                      </td>
                      <td className="px-3 py-2.5 text-[13px] text-slate-600">
                        {o.root_cause || <span className="text-slate-400">not annotated</span>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        )}

        {tab === "inventory" && (
          <div className="grid gap-4 lg:grid-cols-2">
            <Card className="px-4 py-3" title={<span className="text-slate-700">Identity</span>}>
              <dl className="mt-2">
                <Field label="Display name">{r.display_name}</Field>
                <Field label="Native identifier">
                  <span className="font-mono text-[11px] break-all">{r.native_id}</span>
                </Field>
                <Field label="Monitor type">
                  {r.type_name} <span className="text-slate-400">({r.resource_type})</span>
                </Field>
                <Field label="Category">{CATEGORY_LABEL[r.category] ?? r.category}</Field>
                <Field label="Provider">{PROVIDER_LABEL[r.provider] ?? r.provider}</Field>
                <Field label="Region">{r.region || "—"}</Field>
                <Field label="Discovered">{absolute(r.status_since)}</Field>
                <Field label="Last polled">
                  {r.last_polled_at ? `${since(r.last_polled_at)} ago` : "never"}
                </Field>
              </dl>
            </Card>

            <div className="space-y-4">
              <Card className="px-4 py-3" title={<span className="text-slate-700">Attributes</span>}>
                {Object.keys(r.attributes ?? {}).length === 0 ? (
                  <p className="py-3 text-[13px] text-slate-500">No attributes discovered.</p>
                ) : (
                  <dl className="mt-2">
                    {Object.entries(r.attributes).map(([k, v]) => (
                      <Field key={k} label={k.replace(/_/g, " ")}>
                        {Array.isArray(v) ? v.join(", ") : String(v)}
                      </Field>
                    ))}
                  </dl>
                )}
              </Card>

              <Card className="px-4 py-3" title={<span className="text-slate-700">Tags</span>}>
                {Object.keys(r.tags ?? {}).length === 0 ? (
                  <p className="py-3 text-[13px] text-slate-500">No tags.</p>
                ) : (
                  <div className="mt-2 flex flex-wrap gap-1.5 pb-1">
                    {Object.entries(r.tags).map(([k, v]) => (
                      <button
                        key={k}
                        type="button"
                        onClick={() => navigate(`/cloud?tag=${encodeURIComponent(`${k}=${v}`)}`)}
                        className="rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 hover:border-brand-400 hover:text-brand-600"
                      >
                        <span className="text-slate-400">{k}</span>
                        {" : "}
                        {v}
                      </button>
                    ))}
                  </div>
                )}
              </Card>
            </div>
          </div>
        )}

        {tab === "configuration" && (
          <div className="space-y-4">
            <InfoBanner>
              Threshold and notification profiles are defined in the schema but the editor is not
              built yet. The values below are the catalog defaults this monitor is evaluated
              against.
            </InfoBanner>
            <Card className="overflow-hidden" title={<span className="px-1 text-slate-700">Metric thresholds</span>}>
              <table className="mt-2 w-full text-sm">
                <thead>
                  <tr className="border-y border-slate-200 text-left text-[13px] text-slate-600">
                    <th scope="col" className="px-3 py-2 font-medium">Metric</th>
                    <th scope="col" className="px-3 py-2 font-medium">Provider metric</th>
                    <th scope="col" className="px-3 py-2 font-medium">Statistic</th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">Trouble</th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">Critical</th>
                    <th scope="col" className="px-3 py-2 font-medium">Direction</th>
                  </tr>
                </thead>
                <tbody>
                  {type.metrics.map((m, i) => (
                    <tr
                      key={m.key}
                      className={cx("border-b border-slate-100 last:border-0", i % 2 === 1 && "bg-slate-50/70")}
                    >
                      <td className="px-3 py-2 text-slate-800">{m.label}</td>
                      <td className="px-3 py-2 font-mono text-[11px] text-slate-500">
                        {m.namespace}/{m.provider_metric}
                      </td>
                      <td className="px-3 py-2 text-[13px] text-slate-600">{m.statistic}</td>
                      <td className="num px-3 py-2 text-right text-st-trouble">
                        {m.trouble === null ? "—" : metricValue(m.trouble, m.unit)}
                      </td>
                      <td className="num px-3 py-2 text-right text-st-down">
                        {m.critical === null ? "—" : metricValue(m.critical, m.unit)}
                      </td>
                      <td className="px-3 py-2 text-[13px] text-slate-600">
                        {m.higher_is_worse ? "higher is worse" : "lower is worse"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
            <Card className="px-4 py-3" title={<span className="text-slate-700">Polling</span>}>
              <dl className="mt-2">
                <Field label="Poll interval">every {type.default_poll_sec}s</Field>
                <Field label="Availability checks">
                  {type.supports_availability ? "enabled" : "not applicable"}
                </Field>
                <Field label="Cost attribution">
                  {type.supports_cost ? "included in Nimbus FinOps" : "not attributable"}
                </Field>
              </dl>
            </Card>
          </div>
        )}
      </div>
    </>
  );
}

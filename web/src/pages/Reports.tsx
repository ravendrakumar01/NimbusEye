/**
 * Reports.
 *
 * Four reports behind a tab strip, sharing one date range. The range is the only
 * state that survives a tab switch, because "show me the same week, measured a
 * different way" is the actual workflow — re-picking dates for every tab is the
 * kind of friction that stops people using reports at all.
 *
 * Two presentation rules run through the whole page:
 *
 *   A null figure renders as an em dash, never as zero. "Not measured" and "0%"
 *   are different statements and conflating them turns a monitoring gap into a
 *   false accusation against the resource.
 *
 *   Every table states its scope in words. A report is the artefact people
 *   forward to other people, and a number without its window is unreadable once
 *   it leaves the screen it was generated on.
 */

import { useCallback, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { AlertTriangle, Check, Download, X } from "lucide-react";

import { api } from "../lib/api";
import type {
  AvailabilityRow,
  OutageReportRow,
  PerformanceRow,
  ReportQuery,
  SLARow,
} from "../lib/api";
import { useAsync } from "../lib/hooks";
import { absolute, cx, duration, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Select,
  Sparkline,
  Spinner,
  StatTile,
  TabStrip,
} from "../components/ui";

type Tab = "availability" | "performance" | "outages" | "sla";

const TABS: { value: Tab; label: string }[] = [
  { value: "availability", label: "Availability Summary" },
  { value: "performance", label: "Performance" },
  { value: "outages", label: "Outages" },
  { value: "sla", label: "SLA" },
];

/* -------------------------------------------------------------------------- */
/* Date range                                                                  */
/* -------------------------------------------------------------------------- */

/** ISO date in UTC, matching what the API parses. */
function isoDay(d: Date): string {
  return d.toISOString().slice(0, 10);
}

function daysAgo(n: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return isoDay(d);
}

type Preset = "7" | "30" | "90" | "month" | "custom";

const PRESETS: { value: Preset; label: string }[] = [
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
  { value: "month", label: "This month" },
  { value: "custom", label: "Custom" },
];

function presetRange(p: Preset): { from: string; to: string } {
  const today = isoDay(new Date());
  if (p === "month") {
    const d = new Date();
    d.setUTCDate(1);
    return { from: isoDay(d), to: today };
  }
  if (p === "custom") return { from: daysAgo(6), to: today };
  return { from: daysAgo(Number(p) - 1), to: today };
}

/* -------------------------------------------------------------------------- */
/* Shared cells                                                                */
/* -------------------------------------------------------------------------- */

/**
 * Availability figure with a tone.
 *
 * Null is an em dash. Anything at or above 99.9 is unremarkable and stays plain;
 * colour is reserved for figures that need a decision, so that seeing colour on
 * the page means something.
 */
function Availability({ pct }: { pct: number | null }) {
  if (pct === null) {
    return (
      <span className="text-slate-400" title="No availability recorded in this window">
        —
      </span>
    );
  }
  const tone =
    pct >= 99.9
      ? "text-slate-800"
      : pct >= 99
        ? "text-st-trouble"
        : pct >= 95
          ? "text-st-critical"
          : "text-st-down";
  return <span className={cx("font-medium tabular-nums", tone)}>{pct.toFixed(3)}%</span>;
}

/** Downtime, blank when there was none: a column of zeroes is noise. */
function Downtime({ sec }: { sec: number }) {
  if (sec <= 0) return <span className="text-slate-300">—</span>;
  return <span className="tabular-nums text-slate-700">{duration(sec)}</span>;
}

function Th({
  children,
  align = "left",
  w,
}: {
  children: React.ReactNode;
  align?: "left" | "right" | "center";
  w?: string;
}) {
  return (
    <th
      scope="col"
      className={cx(
        "border-b border-slate-200 px-3 py-2 text-[12px] font-medium text-slate-600",
        align === "right" && "text-right",
        align === "center" && "text-center",
        align === "left" && "text-left",
        w,
      )}
    >
      {children}
    </th>
  );
}

function Td({
  children,
  align = "left",
  className,
  title,
}: {
  children: React.ReactNode;
  align?: "left" | "right" | "center";
  className?: string;
  /** Hover text, used to show an absolute timestamp beside a relative one. */
  title?: string;
}) {
  return (
    <td
      title={title}
      className={cx(
        "px-3 py-2 align-middle text-[13px]",
        align === "right" && "text-right",
        align === "center" && "text-center",
        className,
      )}
    >
      {children}
    </td>
  );
}

/** Table shell with zebra striping applied by the parent's tbody. */
function ReportTable({ children }: { children: React.ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse">{children}</table>
    </div>
  );
}

/**
 * Turns the rows on screen into a CSV download.
 *
 * Client-side on purpose: the export then contains exactly the rows and columns
 * the person is looking at, including the filters they applied. A server-side
 * export drifts from the view and people stop trusting it.
 */
function downloadCSV(name: string, header: string[], rows: (string | number)[][]) {
  const esc = (v: string | number) => {
    const s = String(v ?? "");
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  const body = [header, ...rows].map((r) => r.map(esc).join(",")).join("\n");
  const blob = new Blob([body], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/* -------------------------------------------------------------------------- */
/* Availability                                                                */
/* -------------------------------------------------------------------------- */

function AvailabilityReport({ query }: { query: ReportQuery }) {
  const [worst, setWorst] = useState(false);
  const state = useAsync(
    () => api.reportAvailability({ ...query, daily: 1, order: worst ? "worst" : undefined }),
    [query.from, query.to, worst],
  );

  if (state.initialLoading) return <Spinner label="Building the report" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  const d = state.data;
  if (!d) return null;

  const rows = d.rows;
  const byType = Object.entries(d.by_type).sort((a, b) => a[1] - b[1]);

  return (
    <div className="space-y-3 p-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatTile
          label="Mean availability"
          value={d.mean_availability_pct === null ? "—" : `${d.mean_availability_pct.toFixed(3)}%`}
          hint={`across ${d.resources} monitors`}
        />
        <StatTile label="Total downtime" value={d.total_down_sec ? duration(d.total_down_sec) : "none"} />
        <StatTile label="Outages" value={d.total_outages} />
        <StatTile label="Monitors" value={d.resources} hint={`${d.from} to ${d.to}`} />
      </div>

      {byType.length > 0 && (
        <Card title="By resource type">
          <div className="grid grid-cols-2 gap-x-6 gap-y-1 px-3 pt-1 pb-3 md:grid-cols-3">
            {byType.map(([name, pct]) => (
              <div key={name} className="flex items-baseline justify-between gap-2 text-[13px]">
                <span className="truncate text-slate-600">{name}</span>
                <Availability pct={pct} />
              </div>
            ))}
          </div>
        </Card>
      )}

      <Card
        title={`Per monitor — ${d.from} to ${d.to}`}
        action={
          <div className="flex items-center gap-2">
            <Button size="xs" variant={worst ? "primary" : "default"} onClick={() => setWorst(!worst)}>
              Worst first
            </Button>
            <Button
              size="xs"
              onClick={() =>
                downloadCSV(
                  `availability-${d.from}-to-${d.to}.csv`,
                  ["Monitor", "Type", "Provider", "Availability %", "Downtime (s)", "Outages", "MTTR (s)", "Days measured"],
                  rows.map((r) => [
                    r.display_name,
                    r.type_name,
                    r.provider,
                    r.availability_pct ?? "",
                    r.down_sec,
                    r.outage_count,
                    r.mttr_sec ?? "",
                    r.days_with_data,
                  ]),
                )
              }
            >
              <Download className="size-3.5" aria-hidden="true" />
              CSV
            </Button>
          </div>
        }
      >
        {rows.length === 0 ? (
          <EmptyState
            title="No monitors in this scope"
            hint="Availability is recorded once a monitor has been checked for a full day."
          />
        ) : (
          <ReportTable>
            <thead>
              <tr>
                <Th>Monitor</Th>
                <Th>Type</Th>
                <Th align="right">Availability</Th>
                <Th align="right">Downtime</Th>
                <Th align="right">Outages</Th>
                <Th align="right">MTTR</Th>
                <Th align="center">Trend</Th>
                <Th align="right">Days</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r: AvailabilityRow, i) => {
                const series = (r.daily ?? [])
                  .map((p) => p.availability_pct)
                  .filter((v): v is number => v !== null);
                return (
                  <tr key={r.resource_id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                    <Td>
                      <Link
                        to={`/monitor/${r.resource_id}`}
                        className="text-brand-600 hover:underline"
                      >
                        {r.display_name}
                      </Link>
                      {r.region && <div className="text-[11px] text-slate-400">{r.region}</div>}
                    </Td>
                    <Td className="text-slate-600">{r.type_name}</Td>
                    <Td align="right">
                      <Availability pct={r.availability_pct} />
                    </Td>
                    <Td align="right">
                      <Downtime sec={r.down_sec} />
                    </Td>
                    <Td align="right" className="tabular-nums text-slate-700">
                      {r.outage_count || <span className="text-slate-300">—</span>}
                    </Td>
                    <Td align="right" className="tabular-nums text-slate-700">
                      {r.mttr_sec === null ? (
                        <span className="text-slate-300">—</span>
                      ) : (
                        duration(r.mttr_sec)
                      )}
                    </Td>
                    <Td align="center">
                      <Sparkline values={series} />
                    </Td>
                    <Td align="right" className="tabular-nums text-slate-500">
                      {r.days_with_data}
                    </Td>
                  </tr>
                );
              })}
            </tbody>
          </ReportTable>
        )}
      </Card>

      <p className="px-1 text-[11px] leading-relaxed text-slate-500">
        Availability is the mean of the daily figures, not uptime divided by the whole window. A
        monitor added three days ago is measured over those three days — it is neither credited nor
        penalised for the days before it existed. The <span className="font-medium">Days</span>{" "}
        column shows how many days actually carry data.
      </p>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Performance                                                                 */
/* -------------------------------------------------------------------------- */

function PerformanceReportView({ query }: { query: ReportQuery }) {
  const [metric, setMetric] = useState("");
  const state = useAsync(
    () => api.reportPerformance({ ...query, metric: metric || undefined, order: "worst" }),
    [query.from, query.to, metric],
  );

  if (state.initialLoading) return <Spinner label="Building the report" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  const d = state.data;
  if (!d) return null;

  const unit = d.rows[0]?.unit ?? "";
  const fmt = (v: number) => `${v.toLocaleString()}${unit ? ` ${unit}` : ""}`;

  return (
    <div className="space-y-3 p-4">
      <Card
        title={`Metric statistics — ${d.from} to ${d.to}`}
        action={
          <div className="flex items-center gap-2">
            <Select
              value={d.metric_key}
              onChange={(v) => setMetric(v)}
              options={d.available_metrics.map((m) => ({
                value: m.key,
                label: `${m.label} (${m.resource_count})`,
              }))}
              label="Metric"
            />
            {d.rows.length > 0 && (
              <Button
                size="xs"
                onClick={() =>
                  downloadCSV(
                    `performance-${d.metric_key}-${d.from}-to-${d.to}.csv`,
                    ["Monitor", "Type", "Metric", "Unit", "Average", "P95", "Min", "Max", "Samples", "Breaching"],
                    d.rows.map((r) => [
                      r.display_name,
                      r.type_name,
                      r.label,
                      r.unit,
                      r.avg,
                      r.p95,
                      r.min,
                      r.max,
                      r.samples,
                      r.breaching ?? "",
                    ]),
                  )
                }
              >
                <Download className="size-3.5" aria-hidden="true" />
                CSV
              </Button>
            )}
          </div>
        }
      >
        {d.available_metrics.length === 0 ? (
          <EmptyState
            title="No metric data in this window"
            hint="Metrics arrive when the collector or prober runs. A newly added account has none until its first successful collection."
          />
        ) : d.rows.length === 0 ? (
          <EmptyState title="No samples for this metric in the selected range" />
        ) : (
          <ReportTable>
            <thead>
              <tr>
                <Th>Monitor</Th>
                <Th>Type</Th>
                <Th align="right">Average</Th>
                <Th align="right">P95</Th>
                <Th align="right">Min</Th>
                <Th align="right">Max</Th>
                <Th align="right">Samples</Th>
                <Th>State</Th>
              </tr>
            </thead>
            <tbody>
              {d.rows.map((r: PerformanceRow, i) => (
                <tr key={r.resource_id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                  <Td>
                    <Link to={`/monitor/${r.resource_id}`} className="text-brand-600 hover:underline">
                      {r.display_name}
                    </Link>
                  </Td>
                  <Td className="text-slate-600">{r.type_name}</Td>
                  <Td align="right" className="tabular-nums text-slate-700">
                    {fmt(r.avg)}
                  </Td>
                  <Td align="right" className="font-medium tabular-nums text-slate-900">
                    {fmt(r.p95)}
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-500">
                    {fmt(r.min)}
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-500">
                    {fmt(r.max)}
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-500">
                    {r.samples}
                  </Td>
                  <Td>
                    {r.breaching ? (
                      <span
                        className={cx(
                          "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium",
                          r.breaching === "critical"
                            ? "bg-st-critical-bg text-st-critical"
                            : "bg-st-trouble-bg text-st-trouble",
                        )}
                      >
                        <AlertTriangle className="size-3" aria-hidden="true" />
                        {r.breaching}
                      </span>
                    ) : (
                      <span className="text-[11px] text-slate-400">within threshold</span>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </ReportTable>
        )}
      </Card>

      <p className="px-1 text-[11px] leading-relaxed text-slate-500">
        The threshold state is judged on <span className="font-medium">P95</span>, not the maximum.
        One momentary spike is not a sustained problem, and judging on the maximum would flag almost
        everything. Rows are ordered by P95, worst first.
      </p>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Outages                                                                     */
/* -------------------------------------------------------------------------- */

function OutageReportView({ query }: { query: ReportQuery }) {
  const state = useAsync(() => api.reportOutages(query), [query.from, query.to]);

  if (state.initialLoading) return <Spinner label="Building the report" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  const d = state.data;
  if (!d) return null;

  return (
    <div className="space-y-3 p-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatTile label="Outages" value={d.total} hint={`${d.from} to ${d.to}`} />
        <StatTile label="Still open" value={d.ongoing} status={d.ongoing > 0 ? "down" : undefined} />
        <StatTile label="Total downtime" value={d.total_down_sec ? duration(d.total_down_sec) : "none"} />
        <StatTile
          label="Mean time to recover"
          value={d.mean_mttr_sec === null ? "—" : duration(d.mean_mttr_sec)}
          hint={d.mean_mttr_sec === null ? "nothing has recovered yet" : undefined}
        />
      </div>

      <Card
        title="Outages overlapping this window"
        action={
          d.rows.length > 0 ? (
            <Button
              size="xs"
              onClick={() =>
                downloadCSV(
                  `outages-${d.from}-to-${d.to}.csv`,
                  ["Monitor", "Type", "Started", "Ended", "Duration (s)", "Severity", "Classified as", "Root cause"],
                  d.rows.map((r) => [
                    r.display_name,
                    r.type_name,
                    r.started_at,
                    r.ended_at ?? "",
                    r.duration_sec,
                    r.severity,
                    r.classified_as,
                    r.root_cause ?? "",
                  ]),
                )
              }
            >
              <Download className="size-3.5" aria-hidden="true" />
              CSV
            </Button>
          ) : undefined
        }
      >
        {d.rows.length === 0 ? (
          <EmptyState title="No outages in this window" hint="Nothing went down in the selected range." />
        ) : (
          <ReportTable>
            <thead>
              <tr>
                <Th>Monitor</Th>
                <Th>Started</Th>
                <Th>Ended</Th>
                <Th align="right">Duration</Th>
                <Th>Classified</Th>
                <Th>Root cause</Th>
              </tr>
            </thead>
            <tbody>
              {d.rows.map((r: OutageReportRow, i) => (
                <tr key={r.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                  <Td>
                    <Link to={`/monitor/${r.resource_id}`} className="text-brand-600 hover:underline">
                      {r.display_name}
                    </Link>
                    <div className="text-[11px] text-slate-400">{r.type_name}</div>
                  </Td>
                  <Td className="text-slate-600" title={absolute(r.started_at)}>
                    {since(r.started_at)}
                  </Td>
                  <Td className="text-slate-600">
                    {r.ended_at ? (
                      <span title={absolute(r.ended_at)}>{since(r.ended_at)}</span>
                    ) : (
                      <span className="font-medium text-st-down">ongoing</span>
                    )}
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-700">
                    {duration(r.duration_sec)}
                  </Td>
                  <Td>
                    <span
                      className={cx(
                        "rounded px-1.5 py-0.5 text-[11px]",
                        r.classified_as === "outage"
                          ? "bg-st-down-bg text-st-down"
                          : "bg-slate-100 text-slate-600",
                      )}
                    >
                      {r.classified_as}
                    </span>
                  </Td>
                  <Td className="text-slate-500">
                    {r.root_cause || <span className="text-slate-300">not recorded</span>}
                  </Td>
                </tr>
              ))}
            </tbody>
          </ReportTable>
        )}
      </Card>

      <p className="px-1 text-[11px] leading-relaxed text-slate-500">
        An outage is listed when it <span className="font-medium">overlaps</span> the window, not
        only when it falls entirely inside it. A three-day outage therefore still appears in a
        one-day report, which is the behaviour you want when asking "was anything down yesterday".
        Ongoing outages count their duration up to now.
      </p>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* SLA                                                                         */
/* -------------------------------------------------------------------------- */

function SLAReportView({ query }: { query: ReportQuery }) {
  const state = useAsync(() => api.reportSLA(query), [query.from, query.to]);

  if (state.initialLoading) return <Spinner label="Building the report" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  const d = state.data;
  if (!d) return null;

  return (
    <div className="space-y-3 p-4">
      {d.rows.length === 0 ? (
        <Card>
          <EmptyState
            title="No SLA targets defined"
            hint="An SLA pairs a target percentage with a set of monitors. Without one there is nothing to measure against, so this report stays empty rather than inventing a target."
          />
        </Card>
      ) : (
        <Card title={`Compliance — ${d.from} to ${d.to}`}>
          <ReportTable>
            <thead>
              <tr>
                <Th>SLA</Th>
                <Th align="right">Target</Th>
                <Th align="right">Actual</Th>
                <Th align="center">Status</Th>
                <Th align="right">Monitors</Th>
                <Th align="right">Downtime</Th>
                <Th align="right">Error budget</Th>
              </tr>
            </thead>
            <tbody>
              {d.rows.map((r: SLARow, i) => (
                <tr key={r.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                  <Td>
                    <div className="font-medium text-slate-800">{r.display_name}</div>
                    <div className="text-[11px] text-slate-400">{r.period}</div>
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-600">
                    {r.target_pct}%
                  </Td>
                  <Td align="right">
                    <Availability pct={r.actual_pct} />
                  </Td>
                  <Td align="center">
                    {r.compliant === null ? (
                      <span className="text-[11px] text-slate-400" title="Nothing measured in this window">
                        not measured
                      </span>
                    ) : r.compliant ? (
                      <span className="inline-flex items-center gap-1 rounded bg-st-up-bg px-1.5 py-0.5 text-[11px] font-medium text-st-up">
                        <Check className="size-3" aria-hidden="true" />
                        met
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded bg-st-down-bg px-1.5 py-0.5 text-[11px] font-medium text-st-down">
                        <X className="size-3" aria-hidden="true" />
                        breached
                      </span>
                    )}
                  </Td>
                  <Td align="right" className="tabular-nums text-slate-600">
                    {r.resource_count}
                  </Td>
                  <Td align="right">
                    <Downtime sec={r.down_sec} />
                  </Td>
                  <Td align="right">
                    {r.error_budget_sec === null ? (
                      <span className="text-slate-300">—</span>
                    ) : r.error_budget_sec >= 0 ? (
                      <span className="tabular-nums text-slate-700">
                        {duration(r.error_budget_sec)} left
                      </span>
                    ) : (
                      <span className="font-medium tabular-nums text-st-down">
                        {duration(-r.error_budget_sec)} over
                      </span>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </ReportTable>
        </Card>
      )}

      <p className="px-1 text-[11px] leading-relaxed text-slate-500">
        The error budget is the downtime the target still permits over this window, in time rather
        than percent — "14 minutes left" is actionable in a way that "0.03% remaining" is not. An
        SLA with nothing measured reads{" "}
        <span className="font-medium">not measured</span>, never{" "}
        <span className="font-medium">breached</span>: an absent measurement is not a failure.
      </p>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Page                                                                        */
/* -------------------------------------------------------------------------- */

export function Reports() {
  // The tab lives in the URL so the context panel can link straight to a report
  // and so a link someone pastes into a ticket opens the report they meant.
  const [params, setParams] = useSearchParams();
  const urlTab = params.get("tab");
  const tab: Tab = TABS.some((t) => t.value === urlTab) ? (urlTab as Tab) : "availability";
  const setTab = (v: Tab) => {
    const next = new URLSearchParams(params);
    next.set("tab", v);
    setParams(next, { replace: true });
  };
  const [preset, setPreset] = useState<Preset>("7");
  const [range, setRange] = useState(() => presetRange("7"));

  const applyPreset = useCallback((p: Preset) => {
    setPreset(p);
    if (p !== "custom") setRange(presetRange(p));
  }, []);

  // The query object is memoised on its values, not its identity, so a tab switch
  // does not refetch data the new tab already has.
  const query = useMemo<ReportQuery>(() => ({ from: range.from, to: range.to }), [range.from, range.to]);

  const spanDays =
    Math.round(
      (Date.parse(range.to + "T00:00:00Z") - Date.parse(range.from + "T00:00:00Z")) / 86400000,
    ) + 1;
  const reversed = spanDays < 1;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Reports"
        meta={
          reversed
            ? undefined
            : `${range.from} to ${range.to} · ${spanDays} day${spanDays === 1 ? "" : "s"}`
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={preset}
              onChange={(v) => applyPreset(v as Preset)}
              options={PRESETS}
              label="Range"
            />
            {preset === "custom" && (
              <>
                <input
                  type="date"
                  value={range.from}
                  max={range.to}
                  onChange={(e) => setRange((r) => ({ ...r, from: e.target.value }))}
                  aria-label="From date"
                  className="rounded border border-slate-300 px-2 py-1 text-[13px] text-slate-700"
                />
                <span className="text-xs text-slate-400">to</span>
                <input
                  type="date"
                  value={range.to}
                  min={range.from}
                  max={isoDay(new Date())}
                  onChange={(e) => setRange((r) => ({ ...r, to: e.target.value }))}
                  aria-label="To date"
                  className="rounded border border-slate-300 px-2 py-1 text-[13px] text-slate-700"
                />
              </>
            )}
          </div>
        }
      />

      <div className="border-b border-slate-200 px-4">
        <TabStrip tabs={TABS} value={tab} onChange={setTab} label="Report type" />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {reversed ? (
          <div className="p-4">
            <InfoBanner tone="warn">
              The end date is earlier than the start date. Fix the range to run the report — showing
              an empty result here would read as "nothing happened" when it means "the dates are
              swapped".
            </InfoBanner>
          </div>
        ) : (
          <>
            {tab === "availability" && <AvailabilityReport query={query} />}
            {tab === "performance" && <PerformanceReportView query={query} />}
            {tab === "outages" && <OutageReportView query={query} />}
            {tab === "sla" && <SLAReportView query={query} />}
          </>
        )}
      </div>
    </div>
  );
}

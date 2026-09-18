/**
 * Outages, under Home.
 *
 * Separate from the same data on the reports page, because the questions differ.
 * Reports ask "how did last month go"; this asks "what is broken now, and what
 * broke recently". So ongoing outages sort first and the default window is short.
 *
 * An empty page here is a real answer, not a missing feature: it means nothing has
 * gone down. The empty state says that in words rather than leaving a blank panel
 * that reads as a failure to load.
 */

import { useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle } from "lucide-react";

import { api } from "../lib/api";
import type { OutageReportRow } from "../lib/api";
import { useAsync, usePolling } from "../lib/hooks";
import { absolute, cx, duration, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  PageHeader,
  PillGroup,
  Spinner,
  StatTile,
} from "../components/ui";

type Range = "1" | "7" | "30" | "90";

const RANGES: { value: Range; label: string }[] = [
  { value: "1", label: "24 hours" },
  { value: "7", label: "7 days" },
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
];

export function Outages() {
  const [days, setDays] = useState<Range>("30");
  const [ongoingOnly, setOngoingOnly] = useState(false);

  const state = useAsync(
    () => api.outageList({ days: Number(days), ongoing: ongoingOnly ? 1 : undefined, limit: 200 }),
    [days, ongoingOnly],
  );
  // An ongoing outage's duration grows while you watch it, so the page refreshes
  // itself rather than showing a figure that silently goes stale.
  usePolling(state.reload, 60_000, (state.data?.ongoing ?? 0) > 0);

  const d = state.data;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Outages"
        meta={d ? `${d.total} in the last ${days === "1" ? "24 hours" : `${days} days`}` : undefined}
        actions={
          <div className="flex items-center gap-2">
            <Button
              size="xs"
              variant={ongoingOnly ? "primary" : "default"}
              onClick={() => setOngoingOnly(!ongoingOnly)}
            >
              Ongoing only
            </Button>
            <PillGroup value={days} options={RANGES} onChange={setDays} label="Time range" />
          </div>
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.initialLoading ? (
          <Spinner label="Loading outages" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : !d ? null : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatTile label="Outages" value={d.total} />
              <StatTile
                label="Still open"
                value={d.ongoing}
                status={d.ongoing > 0 ? "down" : undefined}
                hint={d.ongoing > 0 ? "happening now" : undefined}
              />
              <StatTile
                label="Total downtime"
                value={d.total_down_sec ? duration(d.total_down_sec) : "none"}
              />
              <StatTile
                label="Mean time to recover"
                value={d.mean_mttr_sec === null ? "—" : duration(d.mean_mttr_sec)}
                hint={d.mean_mttr_sec === null ? "nothing has recovered yet" : "closed outages only"}
              />
            </div>

            <Card title={ongoingOnly ? "Ongoing outages" : "Outages, most recent first"}>
              {d.rows.length === 0 ? (
                <EmptyState
                  title={ongoingOnly ? "Nothing is down right now" : "No outages in this period"}
                  hint="An outage is recorded when a monitor fails enough consecutive checks to be confirmed down, so a single missed poll does not appear here."
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["Monitor", "Started", "Ended", "Duration", "Classified", "Root cause"].map(
                          (h) => (
                            <th
                              key={h}
                              scope="col"
                              className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                            >
                              {h}
                            </th>
                          ),
                        )}
                      </tr>
                    </thead>
                    <tbody>
                      {d.rows.map((o: OutageReportRow, i) => (
                        <tr
                          key={o.id}
                          className={cx(
                            i % 2 ? "bg-slate-50/60" : undefined,
                            !o.ended_at && "bg-st-down-bg/40",
                          )}
                        >
                          <td className="px-3 py-2">
                            <Link
                              to={`/monitor/${o.resource_id}`}
                              className="text-[13px] text-brand-600 hover:underline"
                            >
                              {o.display_name}
                            </Link>
                            <div className="text-[11px] text-slate-400">
                              {o.type_name}
                              {o.region && ` · ${o.region}`}
                            </div>
                          </td>
                          <td
                            className="px-3 py-2 text-[13px] whitespace-nowrap text-slate-600"
                            title={absolute(o.started_at)}
                          >
                            {since(o.started_at)}
                          </td>
                          <td className="px-3 py-2 text-[13px] whitespace-nowrap">
                            {o.ended_at ? (
                              <span className="text-slate-600" title={absolute(o.ended_at)}>
                                {since(o.ended_at)}
                              </span>
                            ) : (
                              <span className="inline-flex items-center gap-1 font-medium text-st-down">
                                <AlertTriangle className="size-3" aria-hidden="true" />
                                ongoing
                              </span>
                            )}
                          </td>
                          <td className="px-3 py-2 text-[13px] tabular-nums text-slate-700">
                            {duration(o.duration_sec)}
                          </td>
                          <td className="px-3 py-2">
                            <span
                              className={cx(
                                "rounded px-1.5 py-0.5 text-[11px]",
                                o.classified_as === "outage"
                                  ? "bg-st-down-bg text-st-down"
                                  : "bg-slate-100 text-slate-600",
                              )}
                              title={
                                o.classified_as === "outage"
                                  ? undefined
                                  : "Excluded from availability because it happened inside a maintenance window"
                              }
                            >
                              {o.classified_as}
                            </span>
                          </td>
                          <td className="px-3 py-2 text-[12px] text-slate-500">
                            {o.root_cause || <span className="text-slate-300">not recorded</span>}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              Mean time to recover covers closed outages only. Including one that is still running
              would average in a recovery that has not happened and make the figure look better than
              it is. An outage inside a maintenance window is classified separately and excluded
              from availability.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

/**
 * Alert Logs.
 *
 * Every attempt to tell somebody about an alert, and what became of it. This is the
 * page that answers "an alarm fired, so why did nobody hear about it" — which is
 * otherwise invisible: the Alarms page shows that a problem was detected, and says
 * nothing about whether the message left the building.
 *
 * On this installation every row currently reads "skipped", because no notification
 * channel is enabled. That is stated at the top rather than left for someone to
 * infer from twenty identical rows.
 */

import { useCallback, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, ArrowRight, Check, Clock, X } from "lucide-react";

import { api } from "../lib/api";
import type { AlertLogEntry } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { absolute, cx, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  PillGroup,
  Spinner,
  StatTile,
} from "../components/ui";

type Filter = "" | "sent" | "failed" | "skipped" | "pending";

const FILTERS: { value: Filter; label: string }[] = [
  { value: "", label: "All" },
  { value: "sent", label: "Sent" },
  { value: "failed", label: "Failed" },
  { value: "skipped", label: "Skipped" },
  { value: "pending", label: "Pending" },
];

function StateChip({ state }: { state: string }) {
  const map: Record<string, { cls: string; icon: typeof Check; title: string }> = {
    sent: { cls: "bg-st-up-bg text-st-up", icon: Check, title: "Delivered to the channel" },
    failed: {
      cls: "bg-st-down-bg text-st-down",
      icon: X,
      title: "The channel rejected it or was unreachable",
    },
    skipped: {
      cls: "bg-slate-100 text-slate-600",
      icon: ArrowRight,
      title: "Nothing was attempted; see the reason",
    },
    pending: { cls: "bg-st-trouble-bg text-st-trouble", icon: Clock, title: "Queued, not yet sent" },
  };
  const m = map[state] ?? map.skipped!;
  const Icon = m.icon;
  return (
    <span
      title={m.title}
      className={cx("inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium", m.cls)}
    >
      <Icon className="size-3" aria-hidden="true" />
      {state}
    </span>
  );
}

export function AlertLogs() {
  const [filter, setFilter] = useState<Filter>("");
  const [pages, setPages] = useState<number[]>([0]);
  const cursor = pages[pages.length - 1] ?? 0;

  const state = useAsync(
    () => api.alertLogs({ state: filter || undefined, before: cursor || undefined, limit: 50 }),
    [filter, cursor],
  );

  const changeFilter = useCallback((v: Filter) => {
    setFilter(v);
    // A new filter is a new sequence; keeping the cursor would land mid-way
    // through a different result set.
    setPages([0]);
  }, []);

  const d = state.data;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Alert Logs"
        meta={pages.length > 1 ? `page ${pages.length}` : undefined}
        actions={<PillGroup value={filter} options={FILTERS} onChange={changeFilter} label="State" />}
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {d && !d.delivery_configured && (
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              <span>
                No notification channel is enabled, so every alert below was recorded and nobody was
                told. The rows read <span className="font-medium">skipped</span> for that reason and
                not because of anything about the alerts themselves.{" "}
                <Link to="/admin/channels" className="font-medium text-brand-600 hover:underline">
                  Add a channel
                </Link>{" "}
                to start delivering.
              </span>
            </span>
          </InfoBanner>
        )}

        {state.initialLoading ? (
          <Spinner label="Loading the log" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : !d ? null : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatTile label="Sent" value={d.sent} />
              <StatTile label="Failed" value={d.failed} status={d.failed > 0 ? "down" : undefined} />
              <StatTile label="Skipped" value={d.skipped} />
              <StatTile label="Pending" value={d.pending} />
            </div>

            <Card title="Delivery attempts, most recent first">
              {d.entries.length === 0 ? (
                <EmptyState
                  title="Nothing logged for this filter"
                  hint="A row appears here each time an alert is eligible to notify somebody."
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["When", "Monitor", "Severity", "Channel", "State", "Detail"].map((h) => (
                          <th
                            key={h}
                            scope="col"
                            className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                          >
                            {h}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {d.entries.map((e: AlertLogEntry, i) => (
                        <tr key={e.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                          <td
                            className="px-3 py-2 text-[13px] whitespace-nowrap text-slate-600"
                            title={absolute(e.created_at)}
                          >
                            {since(e.created_at)}
                          </td>
                          <td className="px-3 py-2">
                            <Link
                              to={`/monitor/${e.resource_id}`}
                              className="text-[13px] text-brand-600 hover:underline"
                            >
                              {e.display_name}
                            </Link>
                            {e.metric_key && (
                              <div className="text-[11px] text-slate-400">{e.metric_key}</div>
                            )}
                          </td>
                          <td className="px-3 py-2">
                            <span
                              className={cx(
                                "text-[12px] font-medium",
                                e.severity === "down"
                                  ? "text-st-down"
                                  : e.severity === "critical"
                                    ? "text-st-critical"
                                    : e.severity === "trouble"
                                      ? "text-st-trouble"
                                      : "text-st-up",
                              )}
                            >
                              {e.severity}
                            </span>
                            {e.level > 0 && (
                              <div
                                className="text-[11px] text-slate-400"
                                title="Escalation level: the first attempt is level 0"
                              >
                                escalation {e.level}
                              </div>
                            )}
                          </td>
                          <td className="px-3 py-2 text-[13px] text-slate-600">
                            {e.channel_name ? (
                              <>
                                {e.channel_name}
                                <div className="text-[11px] text-slate-400">{e.channel_type}</div>
                              </>
                            ) : (
                              <span className="text-slate-300">none</span>
                            )}
                            {e.recipient && (
                              <div className="text-[11px] text-slate-400">{e.recipient}</div>
                            )}
                          </td>
                          <td className="px-3 py-2">
                            <StateChip state={e.state} />
                            {e.attempts > 1 && (
                              <div className="text-[11px] text-slate-400">{e.attempts} attempts</div>
                            )}
                          </td>
                          <td className="max-w-md px-3 py-2 text-[12px] break-words text-slate-500">
                            {e.last_error || <span className="text-slate-300">—</span>}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <div className="flex items-center gap-2 border-t border-slate-200 px-3 py-2.5">
                {pages.length > 1 && (
                  <Button size="xs" onClick={() => setPages(pages.slice(0, -1))}>
                    Previous
                  </Button>
                )}
                {d.next_before > 0 ? (
                  <Button size="xs" onClick={() => setPages([...pages, d.next_before])}>
                    Older entries
                  </Button>
                ) : (
                  <span className="text-[11px] text-slate-400">end of the log</span>
                )}
              </div>
            </Card>

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              A row is written whenever an alert becomes eligible to notify, including when it is
              then skipped. That is deliberate: a log that only recorded successful deliveries could
              not answer why a message never arrived, which is the only question anyone asks of it.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

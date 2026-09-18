/**
 * Alarms: the alert inbox.
 *
 * Designed around one question — what needs a human right now — so the default
 * view is open and acknowledged alarms only, worst and newest first. Resolved
 * alarms are one click away but never in the way.
 */

import { useState } from "react";
import { Link } from "react-router-dom";
import { BellOff, Check, Search, UserCheck } from "lucide-react";
import { ApiError, api } from "../lib/api";
import type { AlarmState, Severity } from "../lib/api";
import { useAsync, useDebounced, usePolling } from "../lib/hooks";
import { absolute, cx, duration, metricValue, num, since } from "../lib/format";
import {
  Button,
  EmptyState,
  ErrorState,
  PageHeader,
  Pagination,
  Select,
  SeverityBadge,
  Spinner,
} from "../components/ui";

const PAGE_SIZE = 25;

export function Alarms() {
  const [severity, setSeverity] = useState<Severity | "">("");
  const [state, setState] = useState<AlarmState | "">("");
  const [provider, setProvider] = useState<string>("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [acking, setAcking] = useState<string | null>(null);
  const [notice, setNotice] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const q = useDebounced(search, 300);

  const alarms = useAsync(
    () =>
      api.alarms({
        severity: severity ? [severity] : undefined,
        state: state ? [state] : undefined,
        provider: provider ? [provider] : undefined,
        q: q || undefined,
        // Selecting "resolved" explicitly has to override the default filter,
        // which otherwise hides exactly what was asked for.
        all: state === "resolved" ? true : undefined,
        page,
        page_size: PAGE_SIZE,
      }),
    [severity, state, provider, q, page],
  );

  usePolling(alarms.reload, 30_000, acking === null);

  async function acknowledge(id: string) {
    setAcking(id);
    setNotice(null);
    try {
      await api.acknowledge(id, "demo-user");
      setNotice({ kind: "ok", text: "Alarm acknowledged." });
      alarms.reload();
    } catch (e) {
      const msg =
        e instanceof ApiError
          ? e.message
          : e instanceof Error
            ? e.message
            : "Could not acknowledge the alarm.";
      setNotice({ kind: "err", text: msg });
    } finally {
      setAcking(null);
    }
  }

  function resetFilters() {
    setSeverity("");
    setState("");
    setProvider("");
    setSearch("");
    setPage(1);
  }

  const filtered = severity || state || provider || q;

  return (
    <div>
      {notice && (
        <div
          role="status"
          className={cx(
            "mx-5 mt-4 flex items-center gap-2 rounded-md border px-3 py-2 text-sm",
            notice.kind === "ok"
              ? "border-st-up/30 bg-st-up-bg text-st-up"
              : "border-st-down/30 bg-st-down-bg text-st-down",
          )}
        >
          {notice.kind === "ok" ? (
            <Check className="size-4" aria-hidden="true" />
          ) : (
            <BellOff className="size-4" aria-hidden="true" />
          )}
          <span>{notice.text}</span>
        </div>
      )}

      <PageHeader
        title={
          <>
            Alarms
            <span className="text-[11px] font-normal text-st-up">Live</span>
          </>
        }
        meta={
          alarms.data
            ? `${num(alarms.data.total)} alarm${alarms.data.total === 1 ? "" : "s"} in view`
            : undefined
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <label className="relative">
              <span className="sr-only">Search alarms</span>
              <Search
                className="pointer-events-none absolute top-2 left-2 size-4 text-slate-400"
                aria-hidden="true"
              />
              <input
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(1);
                }}
                placeholder="Search resource or message"
                className="w-56 rounded-md border border-slate-300 py-1.5 pr-2.5 pl-8 text-sm placeholder:text-slate-400"
              />
            </label>
            <Select
              label="Severity"
              value={severity}
              onChange={(v) => {
                setSeverity(v);
                setPage(1);
              }}
              options={[
                { value: "down", label: "Down" },
                { value: "critical", label: "Critical" },
                { value: "trouble", label: "Trouble" },
              ]}
            />
            <Select
              label="State"
              value={state}
              onChange={(v) => {
                setState(v);
                setPage(1);
              }}
              placeholder="Open & acknowledged"
              options={[
                { value: "open", label: "Open only" },
                { value: "acknowledged", label: "Acknowledged" },
                { value: "resolved", label: "Resolved" },
              ]}
            />
            <Select
              label="Provider"
              value={provider}
              onChange={(v) => {
                setProvider(v);
                setPage(1);
              }}
              options={[
                { value: "oci", label: "Oracle Cloud" },
                { value: "aws", label: "AWS" },
                { value: "azure", label: "Azure" },
                { value: "gcp", label: "Google Cloud" },
                { value: "synthetic", label: "Website" },
              ]}
            />
            {filtered && (
              <Button variant="ghost" size="xs" onClick={resetFilters}>
                Clear
              </Button>
            )}
          </div>
        }
      />
      <div className="px-5 py-4">
        {alarms.initialLoading ? (
          <Spinner label="Loading alarms" />
        ) : alarms.error ? (
          <ErrorState error={alarms.error} onRetry={alarms.reload} />
        ) : !alarms.data?.items.length ? (
          <EmptyState
            title={filtered ? "No alarms match this filter" : "No open alarms"}
            hint={
              filtered
                ? "Try clearing the filters, or look at resolved alarms for history."
                : "Nothing is currently alerting. Resolved alarms are available under the State filter."
            }
          />
        ) : (
          <>
            <div className="overflow-x-auto rounded border border-slate-200 scroll-thin">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs text-slate-500">
                    <th scope="col" className="px-4 py-2 font-medium">
                      Severity
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      Resource
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      Condition
                    </th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">
                      Observed
                    </th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">
                      Threshold
                    </th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">
                      Duration
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      State
                    </th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">
                      Action
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {alarms.data.items.map((a) => {
                    const end = a.resolved_at ? Date.parse(a.resolved_at) : Date.now();
                    const dur = (end - Date.parse(a.opened_at)) / 1000;
                    return (
                      <tr key={a.id} className="align-top hover:bg-slate-50">
                        <td className="px-4 py-2.5">
                          <SeverityBadge severity={a.severity} />
                          {a.escalation_level > 0 && (
                            <div className="mt-1 text-[11px] font-medium text-st-down">
                              Escalated L{a.escalation_level}
                            </div>
                          )}
                        </td>
                        <td className="max-w-[15rem] px-3 py-2.5">
                          <Link
                            to={`/monitor/${a.resource_id}`}
                            className="block truncate font-medium text-brand-500 hover:underline"
                          >
                            {a.resource_name}
                          </Link>
                          <div className="mt-0.5 truncate text-[11px] text-slate-500">
                            <span className="font-semibold uppercase">{a.provider}</span>
                            {a.region && ` · ${a.region}`}
                          </div>
                        </td>
                        <td className="max-w-[18rem] px-3 py-2.5">
                          <div className="text-slate-700">{a.message}</div>
                          <div className="mt-0.5 text-[11px] text-slate-500">
                            confirmed over {a.poll_count} poll{a.poll_count === 1 ? "" : "s"}
                          </div>
                        </td>
                        <td className="num px-3 py-2.5 text-right font-medium text-slate-800">
                          {metricValue(a.observed_value, a.unit)}
                        </td>
                        <td className="num px-3 py-2.5 text-right text-slate-500">
                          {metricValue(a.threshold_value, a.unit)}
                        </td>
                        <td
                          className="num px-3 py-2.5 text-right text-slate-600"
                          title={`Opened ${absolute(a.opened_at)}`}
                        >
                          {duration(dur)}
                        </td>
                        <td className="px-3 py-2.5">
                          {a.state === "acknowledged" ? (
                            <span
                              className="inline-flex items-center gap-1 text-xs text-slate-600"
                              title={a.acknowledged_at ? absolute(a.acknowledged_at) : undefined}
                            >
                              <UserCheck className="size-3.5 text-st-up" aria-hidden="true" />
                              {a.acknowledged_by ?? "acknowledged"}
                            </span>
                          ) : a.state === "resolved" ? (
                            <span className="text-xs text-st-up">
                              Resolved {since(a.resolved_at)} ago
                            </span>
                          ) : (
                            <span className="text-xs font-medium text-st-down">Open</span>
                          )}
                        </td>
                        <td className="px-4 py-2.5 text-right">
                          {a.state === "open" ? (
                            <Button
                              size="xs"
                              onClick={() => acknowledge(a.id)}
                              disabled={acking === a.id}
                            >
                              {acking === a.id ? "Working…" : "Acknowledge"}
                            </Button>
                          ) : (
                            <span className="text-xs text-slate-400">—</span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <Pagination
              page={alarms.data.page}
              pageSize={alarms.data.page_size}
              total={alarms.data.total}
              onPage={setPage}
            />
          </>
        )}
      </div>
    </div>
  );
}

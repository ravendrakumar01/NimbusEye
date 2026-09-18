/**
 * Schedule Maintenance.
 *
 * The alerter has honoured maintenance windows since it was written: a condition
 * that fires inside one is recorded and notifies nobody. What did not exist was any
 * way to create a window without opening psql, which meant the capability was real
 * and unreachable.
 *
 * The window is created against a scope of monitors, and the form refuses a scope
 * that covers nothing — a window that suppresses no alert would sit in this list
 * looking like protection.
 */

import { useCallback, useMemo, useState } from "react";
import { AlertTriangle, CalendarClock, Check, Plus, Trash2, X } from "lucide-react";

import { api } from "../lib/api";
import type { MaintenanceWindow, MonitorGroup, Resource } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { absolute, cx, duration, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Select,
  Spinner,
  StatTile,
} from "../components/ui";

/** Local datetime value for an <input type="datetime-local">. */
function localValue(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const DURATIONS = [
  { value: "30", label: "30 minutes" },
  { value: "60", label: "1 hour" },
  { value: "120", label: "2 hours" },
  { value: "240", label: "4 hours" },
  { value: "480", label: "8 hours" },
];

function StateChip({ state }: { state: string }) {
  if (state === "active") {
    return (
      <span
        className="inline-flex items-center gap-1 rounded bg-st-trouble-bg px-1.5 py-0.5 text-[11px] font-medium text-st-trouble"
        title="Alerts for these monitors are being suppressed right now"
      >
        <CalendarClock className="size-3" aria-hidden="true" />
        active
      </span>
    );
  }
  if (state === "scheduled") {
    return (
      <span className="rounded bg-brand-50 px-1.5 py-0.5 text-[11px] font-medium text-brand-700">
        scheduled
      </span>
    );
  }
  return <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500">finished</span>;
}

function NewWindow({
  resources,
  groups,
  onCreated,
  onCancel,
}: {
  resources: Resource[];
  groups: MonitorGroup[];
  onCreated: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [start, setStart] = useState(() => localValue(new Date(Date.now() + 15 * 60_000)));
  const [mins, setMins] = useState("60");
  const [scopeKind, setScopeKind] = useState<"resources" | "group">("resources");
  const [groupID, setGroupID] = useState(groups[0]?.id ?? "");
  const [picked, setPicked] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    const list = q
      ? resources.filter((r) => r.display_name.toLowerCase().includes(q))
      : resources;
    return list.slice(0, 40);
  }, [resources, search]);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    const startsAt = new Date(start);
    const endsAt = new Date(startsAt.getTime() + Number(mins) * 60_000);
    try {
      await api.createMaintenance({
        display_name: name.trim(),
        starts_at: startsAt.toISOString(),
        ends_at: endsAt.toISOString(),
        resource_ids: scopeKind === "resources" ? picked : [],
        group_ids: scopeKind === "group" && groupID ? [groupID] : [],
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [name, start, mins, scopeKind, picked, groupID, onCreated]);

  const scopeReady = scopeKind === "group" ? groupID !== "" : picked.length > 0;

  return (
    <Card title="Schedule a maintenance window">
      <div className="space-y-4 px-3 pt-2 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">What is happening</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Database patching"
              className="w-64 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Starts</span>
            <input
              type="datetime-local"
              value={start}
              onChange={(e) => setStart(e.target.value)}
              className="rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Lasts</span>
            <Select value={mins} onChange={setMins} options={DURATIONS} label="Duration" />
          </label>
        </div>

        <div className="space-y-2">
          <div className="flex items-center gap-3">
            <span className="text-[12px] text-slate-500">Covers</span>
            <Select
              value={scopeKind}
              onChange={(v) => setScopeKind(v as "resources" | "group")}
              options={[
                { value: "resources", label: "Specific monitors" },
                { value: "group", label: "A monitor group" },
              ]}
              label="Scope"
            />
            {scopeKind === "group" &&
              (groups.length === 0 ? (
                <span className="text-[12px] text-st-trouble">
                  No groups exist yet — create one first, or pick monitors directly.
                </span>
              ) : (
                <Select
                  value={groupID}
                  onChange={setGroupID}
                  options={groups.map((g) => ({
                    value: g.id,
                    label: `${g.display_name} (${g.member_count})`,
                  }))}
                  label="Group"
                />
              ))}
          </div>

          {scopeKind === "resources" && (
            <div className="space-y-1.5">
              <div className="flex items-center gap-2">
                <input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="Search monitors"
                  className="w-64 rounded border border-slate-300 px-2 py-1 text-[13px]"
                />
                <span className="text-[12px] text-slate-500">{picked.length} selected</span>
                {picked.length > 0 && (
                  <Button size="xs" variant="ghost" onClick={() => setPicked([])}>
                    Clear
                  </Button>
                )}
              </div>
              <div className="max-h-48 overflow-y-auto rounded border border-slate-200">
                {visible.length === 0 ? (
                  <p className="p-3 text-[12px] text-slate-500">No monitors match.</p>
                ) : (
                  visible.map((r) => {
                    const on = picked.includes(r.id);
                    return (
                      <button
                        key={r.id}
                        type="button"
                        onClick={() =>
                          setPicked(on ? picked.filter((x) => x !== r.id) : [...picked, r.id])
                        }
                        className={cx(
                          "flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-[13px]",
                          on ? "bg-brand-50 text-brand-800" : "text-slate-700 hover:bg-slate-50",
                        )}
                      >
                        <span
                          className={cx(
                            "flex size-3.5 shrink-0 items-center justify-center rounded border",
                            on ? "border-brand-600 bg-brand-600" : "border-slate-300",
                          )}
                        >
                          {on && <Check className="size-2.5 text-white" aria-hidden="true" />}
                        </span>
                        <span className="truncate">{r.display_name}</span>
                        <span className="ml-auto shrink-0 text-[11px] text-slate-400">
                          {r.type_name}
                        </span>
                      </button>
                    );
                  })
                )}
              </div>
              {resources.length > 40 && !search && (
                <p className="text-[11px] text-slate-400">
                  Showing the first 40 of {resources.length}. Search to narrow.
                </p>
              )}
            </div>
          )}
        </div>

        <InfoBanner>
          While the window is open, alerts for these monitors are recorded but nobody is notified,
          and the downtime is excluded from availability. Nothing stops being monitored — the data
          keeps arriving, so you can still see what happened afterwards.
        </InfoBanner>

        {error && (
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </span>
          </InfoBanner>
        )}

        <div className="flex items-center gap-2">
          <Button variant="primary" disabled={busy || !name.trim() || !scopeReady} onClick={submit}>
            {busy ? "Scheduling…" : "Schedule window"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

export function Maintenance() {
  const { user } = useAuth();
  const canEdit = user.role !== "viewer";
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);

  const windows = useAsync(() => api.maintenanceWindows(), []);
  const monitors = useAsync(() => api.resources({ page_size: 500 }), []);
  const groups = useAsync(() => api.monitorGroups(), []);

  const remove = useCallback(
    async (id: string) => {
      setBusyID(id);
      setError(null);
      try {
        await api.deleteMaintenance(id);
        windows.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyID(null);
      }
    },
    [windows],
  );

  const d = windows.data;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Schedule Maintenance"
        meta={d ? `${d.windows.length} windows` : undefined}
        actions={
          canEdit && !adding ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              Schedule window
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {d && d.active > 0 && (
          <InfoBanner tone="warn">
            {d.active === 1
              ? "One maintenance window is active, so alerts for the monitors it covers are being suppressed right now."
              : `${d.active} maintenance windows are active, so alerts for the monitors they cover are being suppressed right now.`}
          </InfoBanner>
        )}

        {adding && (
          <NewWindow
            resources={monitors.data?.items ?? []}
            groups={groups.data?.items ?? []}
            onCreated={() => {
              setAdding(false);
              windows.reload();
            }}
            onCancel={() => setAdding(false)}
          />
        )}

        {error && (
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </span>
          </InfoBanner>
        )}

        {windows.initialLoading ? (
          <Spinner label="Loading windows" />
        ) : windows.error ? (
          <ErrorState error={windows.error} onRetry={windows.reload} />
        ) : !d ? null : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
              <StatTile label="Active now" value={d.active} status={d.active > 0 ? "trouble" : undefined} />
              <StatTile
                label="Scheduled"
                value={d.windows.filter((w) => w.state === "scheduled").length}
              />
              <StatTile
                label="Finished"
                value={d.windows.filter((w) => w.state === "finished").length}
              />
            </div>

            <Card title="Maintenance windows">
              {d.windows.length === 0 ? (
                <EmptyState
                  title="No maintenance windows"
                  hint="Schedule one before a deployment or a patching run so the resulting alerts do not page anyone, and the downtime does not count against availability."
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["Window", "State", "From", "Until", "Length", "Covers", ""].map((h, i) => (
                          <th
                            key={h || i}
                            scope="col"
                            className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                          >
                            {h}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {d.windows.map((w: MaintenanceWindow, i) => (
                        <tr
                          key={w.id}
                          className={cx(
                            i % 2 ? "bg-slate-50/60" : undefined,
                            w.state === "active" && "bg-st-trouble-bg/40",
                          )}
                        >
                          <td className="px-3 py-2">
                            <div className="text-[13px] font-medium text-slate-800">
                              {w.display_name}
                            </div>
                            {w.created_by && (
                              <div className="text-[11px] text-slate-400">by {w.created_by}</div>
                            )}
                          </td>
                          <td className="px-3 py-2">
                            <StateChip state={w.state} />
                          </td>
                          <td
                            className="px-3 py-2 text-[13px] whitespace-nowrap text-slate-600"
                            title={absolute(w.starts_at)}
                          >
                            {since(w.starts_at)}
                          </td>
                          <td
                            className="px-3 py-2 text-[13px] whitespace-nowrap text-slate-600"
                            title={absolute(w.ends_at)}
                          >
                            {since(w.ends_at)}
                          </td>
                          <td className="px-3 py-2 text-[13px] tabular-nums whitespace-nowrap text-slate-600">
                            {duration(
                              Math.round(
                                (Date.parse(w.ends_at) - Date.parse(w.starts_at)) / 1000,
                              ),
                            )}
                          </td>
                          <td className="px-3 py-2 text-[13px] text-slate-600">
                            <span
                              className={cx(
                                "font-medium",
                                w.resource_count === 0 ? "text-st-down" : "text-slate-800",
                              )}
                            >
                              {w.resource_count}
                            </span>{" "}
                            {w.resource_count === 1 ? "monitor" : "monitors"}
                            {w.sample_names && w.sample_names.length > 0 && (
                              <div className="max-w-xs truncate text-[11px] text-slate-400">
                                {w.sample_names.join(", ")}
                                {w.resource_count > w.sample_names.length && " …"}
                              </div>
                            )}
                          </td>
                          <td className="px-3 py-2 text-right">
                            {canEdit && w.state !== "finished" && (
                              <Button
                                size="xs"
                                disabled={busyID === w.id}
                                onClick={() => remove(w.id)}
                                title={
                                  w.state === "active"
                                    ? "Ends the window now, so alerts resume immediately"
                                    : "Cancels the scheduled window"
                                }
                              >
                                <X className="size-3.5" aria-hidden="true" />
                                {w.state === "active" ? "End now" : "Cancel"}
                              </Button>
                            )}
                            {canEdit && w.state === "finished" && (
                              <Button
                                size="xs"
                                variant="ghost"
                                disabled={busyID === w.id}
                                onClick={() => remove(w.id)}
                              >
                                <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                                <span className="sr-only">Delete {w.display_name}</span>
                              </Button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              A window's state is worked out from the clock each time this page loads, not stored, so
              it cannot drift out of step with reality. Ending an active window takes effect on the
              alerter's next pass, within a minute.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

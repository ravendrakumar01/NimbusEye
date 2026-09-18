/**
 * Business Hours and Tags.
 *
 * Both sit on tables that existed with no way in. Notification profiles have always
 * carried a business_hours_id and could only ever say "always", which is why a
 * trouble-level alert at 3am paged somebody. Tags have been imported from OCI on
 * every collection and nothing displayed them.
 */

import { useCallback, useState } from "react";
import { AlertTriangle, Check, Clock, Plus, Trash2 } from "lucide-react";

import { api } from "../lib/api";
import type { BusinessHours as BH, BusinessHoursSlot, TagKey } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { cx } from "../lib/format";
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

/* -------------------------------------------------------------------------- */
/* Business hours                                                              */
/* -------------------------------------------------------------------------- */

const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

// A short list rather than every zone: these are the ones this deployment
// plausibly needs, and a 400-entry dropdown is worse than a text field.
const ZONES = [
  "Asia/Kolkata",
  "UTC",
  "Europe/London",
  "Europe/Frankfurt",
  "America/New_York",
  "America/Los_Angeles",
  "Asia/Singapore",
  "Australia/Sydney",
];

function slotsSummary(slots: BusinessHoursSlot[]): string {
  const byRange = new Map<string, number[]>();
  for (const s of slots) {
    const k = `${s.start}-${s.end}`;
    byRange.set(k, [...(byRange.get(k) ?? []), s.day]);
  }
  return [...byRange.entries()]
    .map(([range, days]) => {
      const names = days.sort((a, b) => a - b).map((d) => DAYS[d - 1]).join(", ");
      return `${names} ${range.replace("-", "–")}`;
    })
    .join(" · ");
}

function NewSchedule({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [tz, setTz] = useState("Asia/Kolkata");
  const [days, setDays] = useState<number[]>([1, 2, 3, 4, 5]);
  const [start, setStart] = useState("09:30");
  const [end, setEnd] = useState("18:30");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      await api.createBusinessHours({
        display_name: name.trim(),
        timezone: tz,
        slots: days.map((d) => ({ day: d, start, end })),
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [name, tz, days, start, end, onCreated]);

  return (
    <Card title="New schedule">
      <div className="space-y-4 px-3 pt-2 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Office hours"
              className="w-56 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Timezone</span>
            <Select
              value={tz}
              onChange={setTz}
              options={ZONES.map((z) => ({ value: z, label: z }))}
              label="Timezone"
            />
          </label>
        </div>

        <div>
          <div className="mb-1.5 text-[12px] text-slate-500">Days</div>
          <div className="flex flex-wrap gap-1.5">
            {DAYS.map((d, i) => {
              const day = i + 1;
              const on = days.includes(day);
              return (
                <button
                  key={d}
                  type="button"
                  onClick={() => setDays(on ? days.filter((x) => x !== day) : [...days, day])}
                  className={cx(
                    "rounded border px-2.5 py-0.5 text-[12px]",
                    on
                      ? "border-brand-600 bg-brand-50 text-brand-700"
                      : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                  )}
                >
                  {d}
                </button>
              );
            })}
          </div>
        </div>

        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">From</span>
            <input
              type="time"
              value={start}
              onChange={(e) => setStart(e.target.value)}
              className="rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Until</span>
            <input
              type="time"
              value={end}
              onChange={(e) => setEnd(e.target.value)}
              className="rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
        </div>

        <InfoBanner>
          For an overnight shift, create two schedules or two runs of days — a window
          that ends before it starts is refused, because it silently covers nothing.
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
          <Button variant="primary" disabled={busy || !name.trim() || days.length === 0} onClick={submit}>
            {busy ? "Creating…" : "Create schedule"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

export function BusinessHours() {
  const { user } = useAuth();
  const canEdit = user.role === "owner" || user.role === "admin";
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);
  const state = useAsync(() => api.businessHours(), []);

  const remove = useCallback(
    async (id: string) => {
      setBusyID(id);
      setError(null);
      try {
        await api.deleteBusinessHours(id);
        state.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyID(null);
      }
    },
    [state],
  );

  const items = state.data?.schedules ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Business Hours"
        meta={state.data ? `${items.length} ${items.length === 1 ? "schedule" : "schedules"}` : undefined}
        actions={
          canEdit && !adding ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              New schedule
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {adding && (
          <NewSchedule
            onCreated={() => {
              setAdding(false);
              state.reload();
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

        {state.initialLoading ? (
          <Spinner label="Loading schedules" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : items.length === 0 ? (
          <Card>
            <EmptyState
              title="No schedules"
              hint="A notification profile with no schedule notifies around the clock. Define office hours so a trouble-level alert waits until morning while a down alert still wakes somebody."
            />
          </Card>
        ) : (
          <Card title="Schedules">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    {["Schedule", "Timezone", "Windows", "Right now", "Used by", ""].map((h, i) => (
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
                  {items.map((b: BH, i) => (
                    <tr key={b.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                      <td className="px-3 py-2 text-[13px] font-medium text-slate-800">
                        {b.display_name}
                      </td>
                      <td className="px-3 py-2 text-[13px] text-slate-600">{b.timezone}</td>
                      <td className="px-3 py-2 text-[12px] text-slate-600">
                        {slotsSummary(b.slots)}
                      </td>
                      <td className="px-3 py-2">
                        {b.in_hours_now ? (
                          <span className="inline-flex items-center gap-1 rounded bg-st-up-bg px-1.5 py-0.5 text-[11px] font-medium text-st-up">
                            <Check className="size-3" aria-hidden="true" />
                            inside hours
                          </span>
                        ) : (
                          <span className="inline-flex items-center gap-1 text-[11px] text-slate-500">
                            <Clock className="size-3" aria-hidden="true" />
                            outside
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-[13px] tabular-nums">
                        {b.used_by > 0 ? (
                          <span className="text-slate-700">{b.used_by} profile(s)</span>
                        ) : (
                          <span className="text-st-trouble" title="No notification profile uses it, so it has no effect">
                            unused
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right">
                        {canEdit && (
                          <Button
                            size="xs"
                            variant="ghost"
                            disabled={busyID === b.id}
                            onClick={() => remove(b.id)}
                          >
                            <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                            <span className="sr-only">Delete {b.display_name}</span>
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          "Right now" is evaluated in the schedule's own timezone on the server, not
          in your browser, so it reads the same wherever you are. Deleting a schedule a
          profile uses is refused: the reference would become null, which means
          "always", and a profile deliberately limited to office hours would quietly
          start paging at 3am.
        </p>
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Tags                                                                       */
/* -------------------------------------------------------------------------- */

export function Tags() {
  const state = useAsync(() => api.tagInventory(), []);
  const [open, setOpen] = useState<string | null>(null);
  const keys = state.data?.keys ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Tags"
        meta={
          state.data ? `${state.data.key_count} keys, ${state.data.value_count} values` : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.initialLoading ? (
          <Spinner label="Loading tags" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : keys.length === 0 ? (
          <Card>
            <EmptyState
              title="No tags"
              hint="Tags arrive with discovered cloud resources. If your OCI resources carry freeform tags, they will appear here after a collection."
            />
          </Card>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
              <StatTile label="Keys" value={state.data?.key_count ?? 0} />
              <StatTile label="Values" value={state.data?.value_count ?? 0} />
              <StatTile
                label="From the cloud"
                value={keys.filter((k) => k.source === "cloud").length}
                hint="imported by discovery"
              />
            </div>

            <Card title="Tag keys, most used first">
              <ul className="divide-y divide-slate-100">
                {keys.map((k: TagKey) => (
                  <li key={k.key}>
                    <button
                      type="button"
                      onClick={() => setOpen(open === k.key ? null : k.key)}
                      className="flex w-full items-center gap-3 px-3 py-2 text-left hover:bg-slate-50"
                    >
                      <span className="text-[13px] font-medium text-slate-800">{k.key}</span>
                      <span className="rounded bg-slate-100 px-1.5 text-[11px] text-slate-500">
                        {k.source}
                      </span>
                      <span className="text-[12px] text-slate-500">
                        {k.values.length} {k.values.length === 1 ? "value" : "values"}
                      </span>
                      <span className="ml-auto text-[12px] tabular-nums text-slate-600">
                        {k.resources} {k.resources === 1 ? "monitor" : "monitors"}
                      </span>
                    </button>
                    {open === k.key && (
                      <div className="flex flex-wrap gap-1.5 bg-slate-50/60 px-3 pb-2.5">
                        {k.values.map((v) => (
                          <span
                            key={v.id}
                            className="rounded border border-slate-300 bg-white px-2 py-0.5 text-[12px] text-slate-700"
                          >
                            {v.value || <span className="text-slate-400">(empty)</span>}
                            <span className="ml-1.5 text-[11px] text-slate-400">{v.resources}</span>
                          </span>
                        ))}
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            </Card>

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              A key on many monitors is worth grouping or filtering by; the long tail
              usually is not, which is why the list is ordered by use rather than
              alphabetically. Tags marked <span className="font-medium">cloud</span> come
              from the provider and are replaced on each collection, so editing them here
              would not survive.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

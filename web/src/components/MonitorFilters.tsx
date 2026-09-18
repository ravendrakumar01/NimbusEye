/**
 * Filter panel and NOC view for Monitor Status.
 *
 * Both live behind icons in the page header, matching the reference console. They
 * are in their own file because Monitor Status is already the largest page and
 * these are self-contained.
 *
 * The filter panel writes to the URL rather than to component state, so a filtered
 * view can be bookmarked, shared in a ticket, and survives a reload. That is the
 * whole point of filtering an inventory: the useful views are the ones you return
 * to.
 */

import { useMemo } from "react";
import { Link } from "react-router-dom";
import { Check, Filter, Grid3x3, X } from "lucide-react";

import type { Filters, Resource, Status } from "../lib/api";
import { cx } from "../lib/format";
import { Button, StatusDot } from "./ui";

const STATUSES: Status[] = ["down", "critical", "trouble", "up", "unknown", "suspended"];

/* -------------------------------------------------------------------------- */
/* Filter panel                                                                */
/* -------------------------------------------------------------------------- */

/** One toggleable value inside a filter group. */
function Chip({
  label,
  on,
  onClick,
  dot,
}: {
  label: string;
  on: boolean;
  onClick: () => void;
  dot?: Status;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cx(
        "inline-flex items-center gap-1.5 rounded border px-2 py-0.5 text-[12px] transition",
        on
          ? "border-brand-600 bg-brand-50 text-brand-700"
          : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
      )}
    >
      {dot && <StatusDot status={dot} />}
      {on && !dot && <Check className="size-3" aria-hidden="true" />}
      {label}
    </button>
  );
}

export function FilterPanel({
  filters,
  params,
  setMulti,
  setOne,
  onClose,
}: {
  filters: Filters | undefined;
  params: URLSearchParams;
  /** Toggles one value inside a comma-separated parameter. */
  setMulti: (key: string, value: string) => void;
  setOne: (key: string, value: string | undefined) => void;
  onClose: () => void;
}) {
  const selected = (key: string) => (params.get(key) ?? "").split(",").filter(Boolean);

  const providers = filters?.providers ?? [];
  const groups = filters?.groups ?? [];
  const regions = useMemo(() => {
    const all = new Set<string>();
    for (const list of Object.values(filters?.regions ?? {})) {
      for (const r of list) all.add(r);
    }
    return [...all].sort();
  }, [filters]);

  // Types are filtered to the chosen providers: offering all 46 when someone has
  // picked OCI makes the list unusable for the case it exists to serve.
  const chosenProviders = selected("provider");
  const types = useMemo(() => {
    const list = filters?.resource_types ?? [];
    const narrowed =
      chosenProviders.length > 0 ? list.filter((t) => chosenProviders.includes(t.provider)) : list;
    return [...narrowed].sort((a, b) => a.display_name.localeCompare(b.display_name));
  }, [filters, chosenProviders.join(",")]);

  const active =
    selected("provider").length +
    selected("type").length +
    selected("status").length +
    (params.get("group") ? 1 : 0) +
    (params.get("region") ? 1 : 0);

  return (
    <div className="border-b border-slate-200 bg-slate-50/80 px-5 py-3">
      <div className="mb-2 flex items-center gap-2">
        <Filter className="size-3.5 text-slate-500" aria-hidden="true" />
        <span className="text-[13px] font-medium text-slate-700">Filters</span>
        {active > 0 && (
          <span className="rounded bg-brand-500 px-1.5 text-[11px] font-semibold text-white">
            {active}
          </span>
        )}
        <div className="ml-auto flex items-center gap-2">
          {active > 0 && (
            <Button
              size="xs"
              variant="ghost"
              onClick={() => {
                for (const k of ["provider", "type", "status", "group", "region"]) setOne(k, undefined);
              }}
            >
              Clear all
            </Button>
          )}
          <Button size="xs" onClick={onClose}>
            <X className="size-3.5" aria-hidden="true" />
            Close
          </Button>
        </div>
      </div>

      <div className="space-y-2.5">
        <FilterRow label="Status">
          {STATUSES.map((s) => (
            <Chip
              key={s}
              label={s}
              dot={s}
              on={selected("status").includes(s)}
              onClick={() => setMulti("status", s)}
            />
          ))}
        </FilterRow>

        {providers.length > 0 && (
          <FilterRow label="Provider">
            {providers.map((p) => (
              <Chip
                key={p}
                label={p.toUpperCase()}
                on={selected("provider").includes(p)}
                onClick={() => setMulti("provider", p)}
              />
            ))}
          </FilterRow>
        )}

        {types.length > 0 && (
          <FilterRow
            label="Type"
            hint={chosenProviders.length > 0 ? `narrowed to ${chosenProviders.join(", ")}` : undefined}
          >
            {types.slice(0, 24).map((t) => (
              <Chip
                key={t.code}
                label={t.display_name}
                on={selected("type").includes(t.code)}
                onClick={() => setMulti("type", t.code)}
              />
            ))}
          </FilterRow>
        )}

        {regions.length > 0 && (
          <FilterRow label="Region">
            {regions.map((r) => (
              <Chip
                key={r}
                label={r}
                on={selected("region").includes(r)}
                onClick={() => setMulti("region", r)}
              />
            ))}
          </FilterRow>
        )}

        {groups.length > 0 && (
          <FilterRow label="Group">
            {groups.map((g) => (
              <Chip
                key={g.id}
                label={g.display_name}
                on={params.get("group") === g.id}
                onClick={() => setOne("group", params.get("group") === g.id ? undefined : g.id)}
              />
            ))}
          </FilterRow>
        )}
      </div>
    </div>
  );
}

function FilterRow({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1.5">
      <span className="w-16 shrink-0 text-[12px] text-slate-500">{label}</span>
      <div className="flex flex-wrap items-center gap-1.5">{children}</div>
      {hint && <span className="text-[11px] text-slate-400">{hint}</span>}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* NOC view                                                                    */
/* -------------------------------------------------------------------------- */

/**
 * NOC view: one tile per monitor, filling the screen.
 *
 * Built for a wall display, which drives every decision here. No pagination,
 * because nobody clicks a wall. Sorted worst-first, because the only reason to
 * look at it from across a room is to see whether anything is red. Colour carries
 * the whole message and the label is secondary.
 */
export function NOCView({
  resources,
  total,
  onClose,
}: {
  resources: Resource[];
  total: number;
  onClose: () => void;
}) {
  const rank: Record<string, number> = {
    down: 0,
    critical: 1,
    trouble: 2,
    unknown: 3,
    suspended: 4,
    up: 5,
  };
  const sorted = useMemo(
    () =>
      [...resources].sort(
        (a, b) =>
          (rank[a.status] ?? 9) - (rank[b.status] ?? 9) ||
          a.display_name.localeCompare(b.display_name),
      ),
    [resources],
  );

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const r of resources) c[r.status] = (c[r.status] ?? 0) + 1;
    return c;
  }, [resources]);

  const tone: Record<string, string> = {
    down: "bg-st-down text-white",
    critical: "bg-st-critical text-white",
    trouble: "bg-st-trouble text-white",
    up: "bg-st-up/15 text-slate-700",
    unknown: "bg-slate-200 text-slate-600",
    suspended: "bg-slate-100 text-slate-400",
  };

  return (
    <div className="fixed inset-0 z-[60] flex flex-col bg-slate-900">
      <div className="flex items-center gap-4 px-5 py-3">
        <span className="text-[15px] font-medium text-white">Monitor Status</span>
        <div className="flex flex-wrap items-center gap-3 text-[12px]">
          {(["down", "critical", "trouble", "unknown", "suspended", "up"] as const).map((s) =>
            counts[s] ? (
              <span key={s} className="flex items-center gap-1.5 text-slate-300">
                <span className={cx("size-2.5 rounded-full", tone[s]?.split(" ")[0])} />
                {counts[s]} {s}
              </span>
            ) : null,
          )}
        </div>
        <div className="ml-auto flex items-center gap-3">
          {resources.length < total && (
            <span
              className="text-[11px] text-slate-400"
              title="The wall shows the monitors currently loaded on the page behind it"
            >
              showing {resources.length} of {total}
            </span>
          )}
          <button
            type="button"
            onClick={onClose}
            className="rounded border border-slate-600 px-2 py-1 text-[12px] text-slate-300 hover:bg-slate-800"
          >
            Exit NOC view
          </button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8">
          {sorted.map((r) => (
            <Link
              key={r.id}
              to={`/monitor/${r.id}`}
              title={`${r.display_name} — ${r.status} · ${r.type_name}`}
              className={cx(
                "flex min-h-[54px] flex-col justify-center rounded px-2 py-1.5 transition hover:ring-2 hover:ring-white/40",
                tone[r.status] ?? tone.unknown,
              )}
            >
              <span className="truncate text-[12px] leading-tight font-medium">{r.display_name}</span>
              <span className="truncate text-[10px] opacity-75">{r.type_name}</span>
              {r.open_alarms > 0 && (
                <span className="text-[10px] font-semibold">
                  {r.open_alarms} alarm{r.open_alarms === 1 ? "" : "s"}
                </span>
              )}
            </Link>
          ))}
        </div>
        {sorted.length === 0 && (
          <p className="py-16 text-center text-[13px] text-slate-400">
            No monitors match the current filters.
          </p>
        )}
      </div>
    </div>
  );
}

/** The header icon that opens the NOC view, drawn as a labelled grid. */
export function NOCButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="NOC view — a full-screen status wall"
      className="grid size-7 place-items-center rounded text-slate-400 hover:bg-slate-100 hover:text-slate-600"
    >
      <span className="flex flex-col items-center leading-none">
        <Grid3x3 className="size-3.5" aria-hidden="true" />
        <span className="mt-[1px] text-[7px] font-semibold tracking-wide">NOC</span>
      </span>
      <span className="sr-only">Open NOC view</span>
    </button>
  );
}

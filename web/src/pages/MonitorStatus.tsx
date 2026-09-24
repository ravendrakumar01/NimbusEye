/**
 * Monitor Status — the console's primary page pattern.
 *
 * In the product this replicates, Home, Cloud, Web and Kubernetes are all the
 * same page with a different scope: a row of status rings, a set of counters, a
 * polling-window selector, then a sortable monitor table. Building it once and
 * scoping it by provider keeps those four sections genuinely consistent instead
 * of letting them drift into four similar-but-different pages.
 */

import { useMemo } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Filter, Info, Plus, RefreshCw, Search } from "lucide-react";
import { api } from "../lib/api";
import type { Status } from "../lib/api";
import { useState } from "react";
import { useAsync, useDebounced, usePolling } from "../lib/hooks";
import { FilterPanel, NOCButton, NOCView } from "../components/MonitorFilters";
import {
  CATEGORY_LABEL,
  STATUS_LABEL,
  absolute,
  availabilityTone,
  cx,
  num,
  pct,
  since,
} from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Pagination,
  PageHeader,
  PillGroup,
  RowMenu,
  SortHeader,
  Spinner,
  StatusBadge,
  StatusRing,
} from "../components/ui";

const WINDOWS = [
  { value: "polled", label: "Last Polled" },
  { value: "1h", label: "1 Hr" },
  { value: "3h", label: "3 Hrs" },
  { value: "6h", label: "6 Hrs" },
  { value: "12h", label: "12 Hrs" },
  { value: "24h", label: "24 Hrs" },
] as const;

type Window = (typeof WINDOWS)[number]["value"];

export interface MonitorStatusProps {
  /** Restricts the page to these providers. Omitted on Home, which shows all. */
  providers?: string[];
  title?: string;
  /** Lets the Cloud section drive the provider from the URL instead of a prop. */
  providerFromUrl?: boolean;
  /**
   * Restricts the page to these catalog categories.
   *
   * Kubernetes needs this rather than a provider filter: OKE clusters are
   * discovered through OCI and carry provider "oci", so filtering the page by
   * provider "k8s" matched nothing and the section showed an empty list while a
   * cluster sat in alarm. The thing they have in common is the category.
   */
  categories?: string[];
}

const PAGE_SIZE = 25;

export function MonitorStatus({
  providers,
  title = "Monitor Status",
  providerFromUrl = false,
  categories,
}: MonitorStatusProps) {
  const [params, setParams] = useSearchParams();

  const urlProvider = params.get("provider") ?? "";
  const type = params.get("type") ?? "";
  const status = (params.get("status") ?? "") as Status | "";
  const win = (params.get("window") ?? "polled") as Window;
  const sort = (params.get("sort") ?? "status") as "status" | "name" | "availability" | "polled";
  const search = params.get("q") ?? "";
  const page = Math.max(1, Number(params.get("page") ?? "1") || 1);

  const q = useDebounced(search, 300);
  const [showFilters, setShowFilters] = useState(false);
  const [noc, setNoc] = useState(false);

  // Shown on the funnel icon so an active filter is visible without opening it.
  // A filtered list that looks unfiltered is how people conclude monitors have
  // vanished.
  const filterCount =
    (params.get("provider") ?? "").split(",").filter(Boolean).length +
    (params.get("type") ?? "").split(",").filter(Boolean).length +
    (params.get("status") ?? "").split(",").filter(Boolean).length +
    (params.get("region") ?? "").split(",").filter(Boolean).length +
    (params.get("group") ? 1 : 0);

  const scope = providerFromUrl
    ? urlProvider
      ? [urlProvider]
      : (providers ?? [])
    : providers;

  function setParam(key: string, value: string | undefined) {
    const next = new URLSearchParams(params);
    if (!value) next.delete(key);
    else next.set(key, value);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  }

  /**
   * Toggles one value inside a comma-separated parameter.
   *
   * Filters live in the URL rather than in component state so a filtered view can
   * be bookmarked and pasted into a ticket. The useful views are the ones people
   * come back to, and state that dies on reload cannot be one of them.
   */
  function setMulti(key: string, value: string) {
    const current = (params.get(key) ?? "").split(",").filter(Boolean);
    const next = current.includes(value)
      ? current.filter((v) => v !== value)
      : [...current, value];
    setParam(key, next.length ? next.join(",") : undefined);
  }

  const csv = (key: string) => {
    const v = (params.get(key) ?? "").split(",").filter(Boolean);
    return v.length ? v : undefined;
  };

  const filters = useAsync(() => api.filters(), []);

  const list = useAsync(
    () =>
      api.resources({
        q: q || undefined,
        provider: scope && scope.length ? scope : undefined,
        category: categories && categories.length ? categories : undefined,
        type: csv("type"),
        status: csv("status") as Status[] | undefined,
        region: csv("region"),
        group: params.get("group") ?? undefined,
        sort,
        page,
        page_size: PAGE_SIZE,
      }),
    [
      q,
      scope?.join(","),
      categories?.join(","),
      params.get("type"),
      params.get("status"),
      params.get("region"),
      params.get("group"),
      sort,
      page,
    ],
  );

  usePolling(list.reload, 60_000);

  // Recorded once per successful fetch so "Last updated" counts up instead of
  // resetting to "just now" on every re-render.
  const fetchedAt = useMemo(
    () => new Date().toISOString(),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [list.data],
  );

  const typeDef = useMemo(
    () => filters.data?.resource_types.find((t) => t.code === type),
    [filters.data, type],
  );

  const counts = list.data?.counts;
  const byStatus = counts?.by_status ?? {};

  function toggleStatus(s: Status) {
    setParam("status", status === s ? undefined : s);
  }

  return (
    <>
      <PageHeader
        title={
          <>
            {title}
            <button
              type="button"
              onClick={list.reload}
              className="text-slate-400 hover:text-brand-500"
              aria-label="Refresh"
            >
              <RefreshCw
                className={cx("size-3.5", list.loading && "animate-spin")}
                aria-hidden="true"
              />
            </button>
          </>
        }
        meta={list.data ? `Last updated ${since(fetchedAt)} ago` : undefined}
        actions={
          <>
            <label className="relative hidden md:block">
              <span className="sr-only">Search monitors</span>
              <Search
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-slate-400"
                aria-hidden="true"
              />
              <input
                value={search}
                onChange={(e) => setParam("q", e.target.value || undefined)}
                placeholder="Search"
                className="w-44 rounded border border-slate-300 py-1 pr-2 pl-7 text-xs placeholder:text-slate-400"
              />
            </label>
            <button
              type="button"
              onClick={() => setShowFilters((v) => !v)}
              aria-expanded={showFilters}
              title="Filter by status, provider, type, region or group"
              className={cx(
                "relative grid size-7 place-items-center rounded",
                showFilters || filterCount > 0
                  ? "bg-brand-50 text-brand-600"
                  : "text-slate-400 hover:bg-slate-100 hover:text-slate-600",
              )}
            >
              <Filter className="size-4" aria-hidden="true" />
              {filterCount > 0 && (
                <span className="absolute -top-0.5 -right-0.5 grid size-3.5 place-items-center rounded-full bg-brand-500 text-[9px] font-semibold text-white">
                  {filterCount}
                </span>
              )}
              <span className="sr-only">Filters</span>
            </button>
            <NOCButton onClick={() => setNoc(true)} />
            <PillGroup
              label="Polling window"
              value={win}
              options={WINDOWS.map((w) => ({ value: w.value, label: w.label }))}
              onChange={(v) => setParam("window", v === "polled" ? undefined : v)}
            />
            <Link to="/add-monitor">
              <Button size="xs" variant="primary">
                <Plus className="size-3.5" aria-hidden="true" />
                Add Monitor
              </Button>
            </Link>
          </>
        }
      />

      {showFilters && (
        <FilterPanel
          filters={filters.data}
          params={params}
          setMulti={setMulti}
          setOne={setParam}
          onClose={() => setShowFilters(false)}
        />
      )}

      {/* A full-screen wall, rendered above the page rather than inside it so the
          shell chrome does not compete with it from across a room. */}
      {noc && (
        <NOCView
          resources={list.data?.items ?? []}
          total={list.data?.total ?? 0}
          onClose={() => setNoc(false)}
        />
      )}

      {/* Ring row and counters */}
      <div className="flex flex-wrap items-stretch gap-3 px-5 py-4">
        <Card className="flex items-center px-1 py-2">
          {(["down", "critical", "trouble", "up"] as Status[]).map((s) => (
            <StatusRing
              key={s}
              status={s}
              label={STATUS_LABEL[s]}
              count={byStatus[s] ?? 0}
              active={status === s}
              onClick={() => toggleStatus(s)}
            />
          ))}
        </Card>

        <Card className="flex items-center px-1 py-2">
          <StatusRing status="anomaly" label={"Confirmed\nAnomalies"} count={counts?.anomalies ?? 0} />
        </Card>

        <Card className="min-w-[300px] flex-1 px-4 py-3">
          <div className="num text-[13px] text-slate-700">
            Total Monitors: <span className="font-semibold">{num(counts?.total ?? 0)}</span>
          </div>
          <dl className="mt-2 grid grid-cols-2 gap-x-6 gap-y-2">
            {[
              { label: "Maintenance", value: counts?.maintenance ?? 0 },
              { label: "Configuration Error(s)", value: counts?.config_errors ?? 0 },
              { label: "Discovery in Progress", value: counts?.discovery ?? 0 },
              { label: "Suspended Monitors", value: counts?.suspended ?? 0 },
            ].map((c) => (
              <div key={c.label} className="text-center">
                <dd className="num text-lg font-medium text-slate-800">{num(c.value)}</dd>
                <dt className="text-[11px] text-slate-500">{c.label}</dt>
              </div>
            ))}
          </dl>
        </Card>

        <Card
          className="min-w-[240px] px-4 py-3"
          title={
            <span className="inline-flex items-center gap-1 text-slate-700">
              {typeDef?.display_name ?? "All Monitor Types"}
            </span>
          }
        >
          <dl className="mt-3 grid grid-cols-2 gap-x-6">
            <div className="text-center">
              <dd
                className={cx(
                  "num text-lg font-medium",
                  (counts?.open_alarms ?? 0) > 0 ? "text-st-down" : "text-slate-800",
                )}
              >
                {num(counts?.open_alarms ?? 0)}
              </dd>
              <dt className="text-[11px] text-slate-500">Open Alarms</dt>
            </div>
            <div className="text-center">
              <dd
                className={cx(
                  "num text-lg font-medium",
                  availabilityTone(counts?.availability_24h ?? 0),
                )}
              >
                {counts?.availability_24h ? pct(counts.availability_24h) : "—"}
              </dd>
              <dt className="text-[11px] text-slate-500">Availability 24h</dt>
            </div>
          </dl>
          <div className="mt-3 flex items-center justify-between text-[11px] text-slate-500">
            <span className="inline-flex items-center gap-1">
              Monitored resources
              <Info className="size-3 text-slate-400" aria-hidden="true" />
            </span>
            <span className="num">{num(counts?.total ?? 0)}</span>
          </div>
        </Card>
      </div>

      {/* Monitor table */}
      <div className="px-5 pb-6">
        {list.initialLoading ? (
          <Spinner label="Loading monitors" />
        ) : list.error ? (
          <ErrorState error={list.error} onRetry={list.reload} />
        ) : !list.data?.items.length ? (
          <div className="py-6">
            <EmptyState
              title="No active monitors available"
              hint={
                q || status || type
                  ? "No monitors match the current filter."
                  : "You don't have any active monitors in this scope yet."
              }
            />
            {!q && !status && !type && (
              <div className="flex justify-center gap-2">
                <Link to="/add-monitor">
                  <Button variant="primary">Add Monitor</Button>
                </Link>
                <Link to="/admin/cloud-accounts">
                  <Button>Connect a Cloud Account</Button>
                </Link>
              </div>
            )}
          </div>
        ) : (
          <div className="rounded border border-slate-200">
            <table className="w-full">
              <thead>
                <tr className="border-b border-slate-200 bg-white text-left">
                  <th scope="col" className="px-3 py-2.5 pl-11">
                    <SortHeader
                      label="Monitor Name"
                      active={sort === "name"}
                      onClick={() => setParam("sort", sort === "name" ? undefined : "name")}
                    />
                  </th>
                  <th scope="col" className="w-40 px-3 py-2.5">
                    <SortHeader
                      label="Status"
                      active={sort === "status"}
                      onClick={() => setParam("sort", "status")}
                    />
                  </th>
                  <th scope="col" className="w-32 px-3 py-2.5">
                    <SortHeader label="Region" />
                  </th>
                  <th scope="col" className="w-32 px-3 py-2.5">
                    <SortHeader
                      label="Availability"
                      align="right"
                      active={sort === "availability"}
                      onClick={() => setParam("sort", "availability")}
                    />
                  </th>
                  <th scope="col" className="w-24 px-3 py-2.5">
                    <SortHeader label="Alarms" align="right" />
                  </th>
                  <th scope="col" className="w-36 px-3 py-2.5">
                    <SortHeader
                      label="Last Polled"
                      align="right"
                      active={sort === "polled"}
                      onClick={() => setParam("sort", "polled")}
                    />
                  </th>
                  <th scope="col" className="w-10 px-3 py-2.5">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {list.data.items.map((r, i) => (
                  <tr
                    key={r.id}
                    className={cx(
                      "border-b border-slate-100 last:border-0",
                      i % 2 === 1 && "bg-slate-50/70",
                    )}
                  >
                    <td className="px-3 py-2.5">
                      <div className="flex items-start gap-2.5">
                        <span
                          className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-brand-500 text-white"
                          aria-hidden="true"
                        >
                          <Search className="size-3.5" />
                        </span>
                        <div className="min-w-0">
                          <Link
                            to={`/monitor/${r.id}`}
                            className="block max-w-[26rem] truncate text-[13px] text-brand-500 hover:underline"
                            title={r.native_id}
                          >
                            {r.display_name}
                          </Link>
                          <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-slate-500">
                            <span className="truncate">{r.type_name}</span>
                            <span className="text-slate-300">|</span>
                            <span className="uppercase">{r.provider}</span>
                            <span className="text-slate-300">|</span>
                            <span>{CATEGORY_LABEL[r.category] ?? r.category}</span>
                          </div>
                        </div>
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <StatusBadge status={r.status} size="xs" />
                      <div className="num mt-0.5 text-[11px] text-slate-500">
                        for {since(r.status_since)}
                      </div>
                    </td>
                    <td className="px-3 py-2.5 font-mono text-[11px] text-slate-500">{r.region}</td>
                    <td
                      className={cx(
                        "num px-3 py-2.5 text-right text-[13px] font-medium",
                        availabilityTone(r.availability_24h),
                      )}
                    >
                      {r.availability_24h > 0 ? pct(r.availability_24h) : "—"}
                    </td>
                    <td className="num px-3 py-2.5 text-right text-[13px]">
                      {r.open_alarms > 0 ? (
                        <span className="inline-block min-w-5 rounded-full bg-st-down-bg px-1.5 py-0.5 font-semibold text-st-down">
                          {r.open_alarms}
                        </span>
                      ) : (
                        <span className="text-slate-300">0</span>
                      )}
                    </td>
                    <td
                      className="num px-3 py-2.5 text-right text-[12px] text-slate-500"
                      title={absolute(r.last_polled_at)}
                    >
                      {r.last_polled_at ? `${since(r.last_polled_at)} ago` : "never"}
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <RowMenu label={`Actions for ${r.display_name}`} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              page={list.data.page}
              pageSize={list.data.page_size}
              total={list.data.total}
              onPage={(p) => setParam("page", String(p))}
            />
          </div>
        )}
      </div>
    </>
  );
}

/**
 * Cloud account service view.
 *
 * Mirrors the reference console's per-tenancy service grid: one tile per service
 * the provider offers, each showing how many resources of that type this account
 * has, with a toggle to stop collecting it.
 *
 * Types with a zero count are shown rather than hidden. "This service is
 * integrated and empty" and "this service is not being collected" are different
 * facts, and collapsing them is how an operator ends up believing something is
 * monitored when it is not.
 */

import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ChevronRight, Search, Settings2, SlidersHorizontal } from "lucide-react";
import { api } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { CATEGORY_LABEL, PROVIDER_LABEL, cx, num, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
} from "../components/ui";

export function CloudAccountDetail() {
  const { id = "" } = useParams();
  const [search, setSearch] = useState("");
  const [disabled, setDisabled] = useState<Set<string>>(new Set());

  const account = useAsync(() => api.account(id), [id]);
  const services = useAsync(() => api.accountServices(id), [id]);

  const tiles = useMemo(() => {
    const q = search.trim().toLowerCase();
    const list = services.data?.items ?? [];
    const filtered = q
      ? list.filter(
          (t) =>
            t.display_name.toLowerCase().includes(q) ||
            t.category.toLowerCase().includes(q),
        )
      : list;
    // Populated services first: an operator scanning this grid cares about what
    // actually exists before what could exist.
    return [...filtered].sort((a, b) => b.count - a.count || a.display_name.localeCompare(b.display_name));
  }, [services.data, search]);

  if (account.initialLoading) return <Spinner label="Loading account" />;
  if (account.error) return <ErrorState error={account.error} onRetry={account.reload} />;
  if (!account.data) return null;

  const a = account.data;
  const totalResources = tiles.reduce((s, t) => s + t.count, 0);
  const totalUnhealthy = tiles.reduce((s, t) => s + t.unhealthy, 0);

  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-1.5">
            <Link to="/admin/cloud-accounts" className="text-brand-500 hover:underline">
              Cloud Accounts
            </Link>
            <ChevronRight className="size-3.5 text-slate-400" aria-hidden="true" />
            <span className="text-slate-800">Service View · {a.display_name}</span>
          </span>
        }
        actions={
          <>
            <label className="relative">
              <span className="sr-only">Search services</span>
              <Search
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-slate-400"
                aria-hidden="true"
              />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={`Search ${(PROVIDER_LABEL[a.provider] ?? "").split(" ")[0]} services`}
                className="w-52 rounded border border-slate-300 py-1 pr-2 pl-7 text-xs placeholder:text-slate-400"
              />
            </label>
            <Button size="xs">
              <SlidersHorizontal className="size-3.5" aria-hidden="true" />
              Advanced Configuration
            </Button>
            <Link to="/admin/cloud-accounts">
              <Button size="xs">
                <Settings2 className="size-3.5" aria-hidden="true" />
                Edit account
              </Button>
            </Link>
          </>
        }
      />

      <div className="space-y-4 px-5 py-4">
        <div className="flex flex-wrap gap-3">
          {[
            { label: "Services available", value: num(services.data?.total ?? 0) },
            { label: "Services in use", value: num(tiles.filter((t) => t.count > 0).length) },
            { label: "Resources discovered", value: num(totalResources) },
            {
              label: "Unhealthy",
              value: num(totalUnhealthy),
              tone: totalUnhealthy ? "text-st-down" : "text-slate-800",
            },
          ].map((c) => (
            <Card key={c.label} className="min-w-[150px] flex-1 px-4 py-3">
              <div className={cx("num text-xl font-medium", c.tone ?? "text-slate-800")}>
                {c.value}
              </div>
              <div className="mt-0.5 text-[11px] text-slate-500">{c.label}</div>
            </Card>
          ))}
        </div>

        {a.discovery_state === "running" && (
          <InfoBanner>
            Discovery is still running, so these counts are incomplete. Last run started{" "}
            {since(a.last_discovery_at)} ago.
          </InfoBanner>
        )}
        {a.last_error && <InfoBanner tone="warn">{a.last_error}</InfoBanner>}

        {services.initialLoading ? (
          <Spinner label="Loading services" />
        ) : services.error ? (
          <ErrorState error={services.error} onRetry={services.reload} />
        ) : tiles.length === 0 ? (
          <Card className="py-6">
            <EmptyState title="No services match that search" />
          </Card>
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
            {tiles.map((t) => {
              const off = disabled.has(t.resource_type);
              return (
                <div
                  key={t.resource_type}
                  className={cx(
                    "flex flex-col items-center rounded border px-3 py-3 text-center transition",
                    off ? "border-slate-200 bg-slate-50 opacity-60" : "border-slate-200 bg-white",
                  )}
                >
                  <div className="flex min-h-[32px] items-center justify-center">
                    <span className="text-[12px] leading-tight text-slate-700">
                      {t.display_name}
                    </span>
                  </div>
                  <div className="mt-2 flex items-baseline gap-2">
                    <span
                      className={cx(
                        "num text-2xl font-light",
                        t.count === 0 ? "text-slate-300" : "text-slate-800",
                      )}
                    >
                      {num(t.count)}
                    </span>
                    {t.unhealthy > 0 && (
                      <span className="num rounded-full bg-st-down-bg px-1.5 py-0.5 text-[10px] font-semibold text-st-down">
                        {num(t.unhealthy)}
                      </span>
                    )}
                  </div>
                  <div className="mt-0.5 text-[10px] text-slate-400">
                    {CATEGORY_LABEL[t.category] ?? t.category}
                  </div>

                  {t.count > 0 ? (
                    <Link
                      to={`/cloud?provider=${a.provider}&type=${t.resource_type}`}
                      className="mt-2 text-[11px] text-brand-500 hover:underline"
                    >
                      View monitors
                    </Link>
                  ) : (
                    <span className="mt-2 text-[11px] text-slate-400">none found</span>
                  )}

                  <button
                    type="button"
                    onClick={() =>
                      setDisabled((cur) => {
                        const next = new Set(cur);
                        if (next.has(t.resource_type)) next.delete(t.resource_type);
                        else next.add(t.resource_type);
                        return next;
                      })
                    }
                    className="mt-2 w-full rounded border border-slate-300 px-2 py-1 text-[11px] text-slate-600 hover:bg-slate-50"
                  >
                    {off ? "Enable Integration" : "Disable Integration"}
                  </button>
                </div>
              );
            })}
          </div>
        )}

        {disabled.size > 0 && (
          <InfoBanner tone="warn">
            {disabled.size} service{disabled.size === 1 ? "" : "s"} toggled off in this view only.
            Persisting per-service collection settings needs the collector, which is not built yet —
            nothing has actually been disabled.
          </InfoBanner>
        )}
      </div>
    </>
  );
}

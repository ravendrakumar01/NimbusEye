/**
 * Cloud section: Inventory Dashboard and Service View.
 *
 * The Inventory Dashboard reports monitored against discovered, not just monitored.
 * A dashboard that counts only what it watches cannot tell you what it is missing,
 * and on this tenancy that gap is the interesting number: fifteen thousand objects
 * found, a hundred and fifty monitored, and a handful of types that are neither
 * watched nor deliberately ignored.
 *
 * The reference console draws a world map here. A region table carries the same
 * information without inventing a geocoding dependency, and reads faster.
 */

import { useMemo } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { AlertTriangle, ChevronRight } from "lucide-react";

import { api } from "../lib/api";
import type { CloudAccount, InventoryType, ServiceTile } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { cx, num, since } from "../lib/format";
import {
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
  StatTile,
  StatusDot,
} from "../components/ui";
import type { Status } from "../lib/api";

/**
 * Resolves which account the Cloud section is looking at.
 *
 * The provider comes from the URL so the rail's provider tabs work, and the account
 * is the first one for that provider. With one account per provider — which is the
 * normal case — this needs no picker.
 */
function useCloudAccount(): {
  account: CloudAccount | undefined;
  loading: boolean;
  error?: Error;
  provider: string;
} {
  const [params] = useSearchParams();
  const provider = params.get("provider") ?? "oci";
  const accounts = useAsync(() => api.accounts(), []);
  const account = useMemo(
    () => (accounts.data?.items ?? []).find((a) => a.provider === provider),
    [accounts.data, provider],
  );
  return {
    account,
    loading: accounts.initialLoading,
    error: accounts.error,
    provider,
  };
}

/** A horizontal bar, used for the type footprint. */
function Bar({ value, max, tone }: { value: number; max: number; tone: string }) {
  const pct = max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0;
  return (
    <span className="inline-block h-2 w-full max-w-[120px] rounded-sm bg-slate-100 align-middle">
      <span className={cx("block h-2 rounded-sm", tone)} style={{ width: `${pct}%` }} />
    </span>
  );
}

export function CloudInventory() {
  const { account, loading, error, provider } = useCloudAccount();
  const inv = useAsync(
    () => (account ? api.cloudInventory(account.id) : Promise.resolve(undefined)),
    [account?.id],
  );

  if (loading) return <Spinner label="Loading accounts" />;
  if (error) return <ErrorState error={error} />;
  if (!account) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <PageHeader title="Inventory Dashboard" />
        <div className="p-4">
          <Card>
            <EmptyState
              title={`No ${provider.toUpperCase()} account connected`}
              hint="Connect one under Admin, Cloud Accounts, and its inventory will be discovered on the next collection."
            />
          </Card>
        </div>
      </div>
    );
  }

  const d = inv.data;
  const maxCount = d ? Math.max(...d.types.map((t) => t.count), 1) : 1;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title={`Inventory Dashboard — ${account.display_name}`}
        meta={d?.last_run_at ? `discovered ${since(d.last_run_at)}` : undefined}
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {inv.initialLoading ? (
          <Spinner label="Building the inventory" />
        ) : inv.error ? (
          <ErrorState error={inv.error} onRetry={inv.reload} />
        ) : !d ? null : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatTile
                label="Found in the account"
                value={num(d.discovered)}
                hint="by the last discovery"
              />
              <StatTile label="Monitored" value={num(d.monitored)} />
              <StatTile
                label="Ignored as noise"
                value={num(d.ignored)}
                hint="container images, backups"
              />
              <StatTile
                label="Not monitored"
                value={num(d.unmapped_total)}
                status={d.unmapped_total > 0 ? "trouble" : undefined}
                hint={`${d.unmapped.length} types`}
              />
            </div>

            {d.unmapped_total > 0 && (
              <InfoBanner>
                <span className="flex items-start gap-1.5">
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                  <span>
                    {num(d.unmapped_total)} resources across {d.unmapped.length} types exist in
                    this account and nothing here watches them.{" "}
                    <Link to="/discovered" className="font-medium text-brand-600 hover:underline">
                      See which
                    </Link>
                    .
                  </span>
                </span>
              </InfoBanner>
            )}

            <Card title="Monitored resources by type">
              {d.types.length === 0 ? (
                <EmptyState title="Nothing monitored in this account yet" />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["Type", "Count", "", "Up", "Issues", "Unknown", "Suspended", "Regions"].map(
                          (h, i) => (
                            <th
                              key={h || i}
                              scope="col"
                              className={cx(
                                "border-b border-slate-200 px-3 py-2 text-[12px] font-medium text-slate-600",
                                i >= 1 && i <= 6 ? "text-right" : "text-left",
                              )}
                            >
                              {h}
                            </th>
                          ),
                        )}
                      </tr>
                    </thead>
                    <tbody>
                      {d.types.map((t: InventoryType, i) => {
                        const issues = t.down + t.critical + t.trouble;
                        return (
                          <tr key={t.code} className={i % 2 ? "bg-slate-50/60" : undefined}>
                            <td className="px-3 py-2">
                              <Link
                                to={`/cloud?provider=${account.provider}&type=${t.code}`}
                                className="text-[13px] text-brand-600 hover:underline"
                              >
                                {t.display_name}
                              </Link>
                            </td>
                            <td className="px-3 py-2 text-right text-[13px] font-medium tabular-nums text-slate-800">
                              {t.count}
                            </td>
                            <td className="px-3 py-2">
                              <Bar
                                value={t.count}
                                max={maxCount}
                                tone={issues > 0 ? "bg-st-down" : "bg-brand-500"}
                              />
                            </td>
                            <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-600">
                              {t.up || <span className="text-slate-300">—</span>}
                            </td>
                            <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                              {issues ? (
                                <span className="font-medium text-st-down">{issues}</span>
                              ) : (
                                <span className="text-slate-300">—</span>
                              )}
                            </td>
                            <td
                              className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-500"
                              title="Discovered but nothing has measured it, usually because the provider publishes no metric for this type"
                            >
                              {t.unknown || <span className="text-slate-300">—</span>}
                            </td>
                            <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-400">
                              {t.suspended || <span className="text-slate-300">—</span>}
                            </td>
                            <td className="px-3 py-2 text-[12px] text-slate-500">
                              {t.regions.join(", ")}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            {d.regions.length > 0 && (
              <Card title="By region">
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["Region", "Resources", "Types"].map((h, i) => (
                          <th
                            key={h}
                            scope="col"
                            className={cx(
                              "border-b border-slate-200 px-3 py-2 text-[12px] font-medium text-slate-600",
                              i === 0 ? "text-left" : "text-right",
                            )}
                          >
                            {h}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {d.regions.map((rg, i) => (
                        <tr key={rg.region} className={i % 2 ? "bg-slate-50/60" : undefined}>
                          <td className="px-3 py-2 text-[13px] text-slate-700">{rg.region}</td>
                          <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-700">
                            {rg.count}
                          </td>
                          <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-500">
                            {rg.types}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Card>
            )}

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              "Found in the account" is what the last discovery saw, not what is monitored.
              Most of a cloud Resource Search is container image versions and backup
              artefacts, which is why deliberate noise is counted separately from the types
              that are simply not supported yet.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Service View                                                                */
/* -------------------------------------------------------------------------- */

/**
 * Service View: one card per resource type the provider offers.
 *
 * The endpoint behind this has existed since the store was written and nothing
 * called it. It answers a different question from the inventory table: not "what do
 * I have" but "what could this account be monitoring that it is not".
 */
export function CloudServices() {
  const { account, loading, error, provider } = useCloudAccount();
  const services = useAsync(
    () => (account ? api.accountServices(account.id) : Promise.resolve(undefined)),
    [account?.id],
  );

  if (loading) return <Spinner label="Loading accounts" />;
  if (error) return <ErrorState error={error} />;
  if (!account) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <PageHeader title="Service View" />
        <div className="p-4">
          <Card>
            <EmptyState title={`No ${provider.toUpperCase()} account connected`} />
          </Card>
        </div>
      </div>
    );
  }

  const items = services.data?.items ?? [];
  const active = items.filter((t) => t.count > 0);
  const idle = items.filter((t) => t.count === 0);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title={`Service View — ${account.display_name}`}
        meta={`${active.length} of ${items.length} services in use`}
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {services.initialLoading ? (
          <Spinner label="Loading services" />
        ) : services.error ? (
          <ErrorState error={services.error} onRetry={services.reload} />
        ) : (
          <>
            <Card title="In use">
              {active.length === 0 ? (
                <EmptyState title="No services in use yet" />
              ) : (
                <div className="grid grid-cols-2 gap-3 p-3 sm:grid-cols-3 lg:grid-cols-4">
                  {active.map((t: ServiceTile) => (
                    <Link
                      key={t.resource_type}
                      to={`/cloud?provider=${account.provider}&type=${t.resource_type}`}
                      className="rounded border border-slate-200 p-3 transition hover:border-brand-400 hover:bg-brand-50/40"
                    >
                      <div className="flex items-start justify-between gap-2">
                        <span className="text-[13px] font-medium text-slate-800">
                          {t.display_name}
                        </span>
                        <ChevronRight className="size-3.5 shrink-0 text-slate-300" aria-hidden="true" />
                      </div>
                      <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-[22px] leading-none font-semibold text-slate-900 tabular-nums">
                          {t.count}
                        </span>
                        {t.unhealthy > 0 && (
                          <span className="inline-flex items-center gap-1 text-[11px] font-medium text-st-down">
                            <StatusDot status={"down" as Status} />
                            {t.unhealthy} unhealthy
                          </span>
                        )}
                      </div>
                    </Link>
                  ))}
                </div>
              )}
            </Card>

            {idle.length > 0 && (
              <Card title={`Available and not in use (${idle.length})`}>
                <div className="flex flex-wrap gap-1.5 p-3">
                  {idle.map((t) => (
                    <span
                      key={t.resource_type}
                      className="rounded border border-slate-200 px-2 py-0.5 text-[12px] text-slate-500"
                      title="Supported by this tool; nothing of this type was discovered in the account"
                    >
                      {t.display_name}
                    </span>
                  ))}
                </div>
              </Card>
            )}

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              The second list is types this tool supports and found none of. It is not a
              gap in the tool; it usually means the account does not use that service.
              Types the account does use and this tool does not support are on{" "}
              <Link to="/discovered" className="text-brand-600 hover:underline">
                Discovered Resources
              </Link>
              .
            </p>
          </>
        )}
      </div>
    </div>
  );
}

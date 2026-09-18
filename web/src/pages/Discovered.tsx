/**
 * Discovered Resources and Bulk Action.
 *
 * Discovered Resources answers the question a monitoring tool should be able to
 * answer and usually cannot: what does this account contain that nobody is
 * watching. The collector has worked it out on every run since it was written and
 * only ever put it in a log line.
 *
 * On the live tenancy the numbers are worth seeing plainly — roughly 15,400 objects
 * found, 149 monitored, 15,180 deliberately ignored as noise, and a handful of types
 * that are neither: API gateways, bastions, DRGs, NAT and service gateways. Some of
 * those are worth monitoring, and nobody could see that they existed.
 */

import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, Eye, EyeOff, Pause, Play, Trash2 } from "lucide-react";

import { api } from "../lib/api";
import type { BulkActionResult, DiscoveryInventory, Resource } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { absolute, cx, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
  StatTile,
  StatusDot,
} from "../components/ui";

/* -------------------------------------------------------------------------- */
/* Discovered resources                                                        */
/* -------------------------------------------------------------------------- */

export function Discovered() {
  const state = useAsync(() => api.discovered(), []);
  const accounts = state.data?.accounts ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader title="Discovered Resources" />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.initialLoading ? (
          <Spinner label="Loading inventory" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : accounts.length === 0 ? (
          <Card>
            <EmptyState
              title="No cloud accounts connected"
              hint="Connect an account under Admin to have its inventory discovered."
            />
          </Card>
        ) : (
          accounts.map((a: DiscoveryInventory) => (
            <div key={a.account_id} className="space-y-3">
              <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
                <StatTile
                  label="Found"
                  value={a.discovered.toLocaleString()}
                  hint={a.last_run_at ? `scanned ${since(a.last_run_at)}` : "never scanned"}
                />
                <StatTile label="Monitored" value={a.monitored} />
                <StatTile
                  label="Ignored as noise"
                  value={a.ignored.toLocaleString()}
                  hint="container images, backups"
                />
                <StatTile
                  label="Not monitored"
                  value={a.unmapped_total}
                  status={a.unmapped_total > 0 ? "trouble" : undefined}
                  hint={`${a.unmapped.length} types`}
                />
              </div>

              <Card
                title={`${a.account_name} — types discovery found and does not monitor`}
                action={
                  a.last_run_at ? (
                    <span className="text-[11px] text-slate-400" title={absolute(a.last_run_at)}>
                      last run {since(a.last_run_at)}
                      {a.status && a.status !== "ok" && (
                        <span className="ml-1.5 text-st-trouble">({a.status})</span>
                      )}
                    </span>
                  ) : undefined
                }
              >
                {a.unmapped.length === 0 ? (
                  <EmptyState
                    title="Everything discovered is either monitored or deliberately ignored"
                    hint={
                      a.discovered === 0
                        ? "No discovery has run for this account yet."
                        : undefined
                    }
                  />
                ) : (
                  <>
                    <div className="overflow-x-auto">
                      <table className="w-full border-collapse">
                        <thead>
                          <tr className="bg-slate-50">
                            <th
                              scope="col"
                              className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                            >
                              Provider type
                            </th>
                            <th
                              scope="col"
                              className="border-b border-slate-200 px-3 py-2 text-right text-[12px] font-medium text-slate-600"
                            >
                              Resources
                            </th>
                          </tr>
                        </thead>
                        <tbody>
                          {a.unmapped.map((u, i) => (
                            <tr key={u.provider_type} className={i % 2 ? "bg-slate-50/60" : undefined}>
                              <td className="px-3 py-2 font-mono text-[13px] text-slate-700">
                                {u.provider_type}
                              </td>
                              <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-700">
                                {u.count}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                    <div className="border-t border-slate-200 px-3 py-2.5">
                      <InfoBanner>
                        These exist in the tenancy and nothing here watches them. Supporting
                        a type means mapping it to a monitor and finding metrics the
                        provider actually publishes for it — which is not always any. The
                        list is here so the gap is visible rather than assumed away.
                      </InfoBanner>
                    </div>
                  </>
                )}
              </Card>
            </div>
          ))
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          "Ignored as noise" is deliberate and is kept separate from "not monitored" on
          purpose. Most of what a cloud Resource Search returns is container image
          versions and backup artefacts; folding those into the actionable list would
          bury it under thousands of rows nobody will ever act on.
        </p>
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Bulk action                                                                 */
/* -------------------------------------------------------------------------- */

export function BulkAction() {
  const { user } = useAuth();
  const canEdit = user.role !== "viewer";
  const [picked, setPicked] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [onlySuspended, setOnlySuspended] = useState(false);
  const [result, setResult] = useState<BulkActionResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const list = useAsync(() => api.resources({ page_size: 500 }), []);
  const all = list.data?.items ?? [];

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return all
      .filter((r) => (onlySuspended ? r.suspended : true))
      .filter((r) => (q ? r.display_name.toLowerCase().includes(q) : true))
      .slice(0, 200);
  }, [all, search, onlySuspended]);

  const run = useCallback(
    async (action: string) => {
      setBusy(true);
      setError(null);
      setResult(null);
      try {
        const res = await api.bulkAction(action, picked);
        setResult(res);
        setPicked([]);
        list.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [picked, list],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Bulk Action"
        meta={`${picked.length} of ${all.length} selected`}
        actions={
          canEdit ? (
            <div className="flex items-center gap-2">
              <Button disabled={busy || picked.length === 0} onClick={() => run("suspend")}>
                <Pause className="size-3.5" aria-hidden="true" />
                Suspend
              </Button>
              <Button disabled={busy || picked.length === 0} onClick={() => run("activate")}>
                <Play className="size-3.5" aria-hidden="true" />
                Activate
              </Button>
              <Button
                variant="danger"
                disabled={busy || picked.length === 0}
                onClick={() => run("delete")}
                title="Only monitors created here can be deleted; discovered ones come back"
              >
                <Trash2 className="size-3.5" aria-hidden="true" />
                Delete
              </Button>
            </div>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {!canEdit && (
          <InfoBanner>
            You are signed in as {user.role}. Bulk actions need the operator role or above.
          </InfoBanner>
        )}

        {result && (
          <InfoBanner tone={result.skipped ? "warn" : "info"}>
            <div className="space-y-1">
              <div className="flex items-center gap-1.5">
                <Check className="size-3.5" aria-hidden="true" />
                <span>
                  {result.action}: applied to {result.applied} of {result.requested}.
                  {result.applied === 0 && result.requested > 0 && !result.skipped && (
                    <span className="text-slate-500"> Nothing changed — they were already in that state.</span>
                  )}
                </span>
              </div>
              {result.skipped &&
                Object.entries(result.skipped).map(([name, why]) => (
                  <div key={name} className="pl-5 text-[12px] text-slate-600">
                    <span className="font-medium">{name}</span>: {why}
                  </div>
                ))}
            </div>
          </InfoBanner>
        )}

        {error && (
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </span>
          </InfoBanner>
        )}

        <Card
          title="Select monitors"
          action={
            <div className="flex items-center gap-2">
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search"
                className="w-48 rounded border border-slate-300 px-2 py-1 text-[12px]"
              />
              <Button size="xs" onClick={() => setOnlySuspended(!onlySuspended)}>
                {onlySuspended ? (
                  <>
                    <EyeOff className="size-3.5" aria-hidden="true" />
                    Suspended only
                  </>
                ) : (
                  <>
                    <Eye className="size-3.5" aria-hidden="true" />
                    All
                  </>
                )}
              </Button>
              <Button
                size="xs"
                onClick={() =>
                  setPicked(
                    picked.length === visible.length ? [] : visible.map((r) => r.id),
                  )
                }
              >
                {picked.length === visible.length && visible.length > 0
                  ? "Deselect all"
                  : "Select all shown"}
              </Button>
            </div>
          }
        >
          {list.initialLoading ? (
            <Spinner label="Loading monitors" />
          ) : list.error ? (
            <ErrorState error={list.error} onRetry={list.reload} />
          ) : (
            <>
              <div className="max-h-[52vh] overflow-y-auto">
                {visible.map((r: Resource) => {
                  const on = picked.includes(r.id);
                  return (
                    <button
                      key={r.id}
                      type="button"
                      disabled={!canEdit}
                      onClick={() =>
                        setPicked(on ? picked.filter((x) => x !== r.id) : [...picked, r.id])
                      }
                      className={cx(
                        "flex w-full items-center gap-2.5 border-b border-slate-100 px-3 py-1.5 text-left text-[13px] last:border-0",
                        on ? "bg-brand-50" : "hover:bg-slate-50",
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
                      <StatusDot status={r.status} />
                      <span className="truncate text-slate-800">{r.display_name}</span>
                      {r.suspended && (
                        <span className="rounded bg-slate-100 px-1.5 text-[11px] text-slate-500">
                          suspended
                        </span>
                      )}
                      <span className="ml-auto shrink-0 text-[11px] text-slate-400">
                        {r.type_name}
                      </span>
                      <span
                        className="shrink-0 text-[11px] text-slate-400"
                        title={
                          r.cloud_account_id
                            ? "Discovered from a cloud account — cannot be deleted, only suspended"
                            : "Created here"
                        }
                      >
                        {r.cloud_account_id ? "discovered" : "own check"}
                      </span>
                    </button>
                  );
                })}
              </div>
              {all.length > visible.length && (
                <p className="border-t border-slate-200 px-3 py-2 text-[11px] text-slate-400">
                  Showing {visible.length} of {all.length}. Search to narrow.
                </p>
              )}
            </>
          )}
        </Card>

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          Activating a monitor sets it back to{" "}
          <span className="font-medium">unknown</span>, not up: nothing has measured it
          while it was suspended, so claiming it is up would be a statement nothing
          supports. Discovered monitors cannot be deleted — they reappear on the next
          collection — so the action reports them as skipped with the reason instead of
          pretending to work. Requests are capped at 500 monitors.{" "}
          <Link to="/" className="text-brand-600 hover:underline">
            Monitor Status
          </Link>{" "}
          has per-monitor actions.
        </p>
      </div>
    </div>
  );
}

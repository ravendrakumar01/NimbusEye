/**
 * SLO — availability targets.
 *
 * The SLA tab of the reports page reads these, and until now there was no way to
 * create one, so that report could only ever be empty. This is the missing half.
 *
 * Every target is shown twice: as a percentage, and as the downtime it actually
 * permits per day. The second form is the one people can check against reality —
 * "eight and a half minutes a day" is a judgement anyone can make, and "99.4%" is
 * not.
 */

import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, Plus, Trash2 } from "lucide-react";

import { api } from "../lib/api";
import type { MonitorGroup, Resource, SLATarget } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { cx, duration } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Select,
  Spinner,
} from "../components/ui";

const PERIODS = [
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
  { value: "quarterly", label: "Quarterly" },
];

/** Common targets, with the downtime each one allows spelled out. */
const PRESETS = [
  { pct: 99, label: "99%" },
  { pct: 99.5, label: "99.5%" },
  { pct: 99.9, label: "99.9%" },
  { pct: 99.95, label: "99.95%" },
  { pct: 99.99, label: "99.99%" },
];

function allowedPerDay(pct: number): number {
  return Math.round((86400 * (100 - pct)) / 100);
}

function NewTarget({
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
  const [pct, setPct] = useState(99.9);
  const [period, setPeriod] = useState("monthly");
  const [scopeKind, setScopeKind] = useState<"resources" | "group">("resources");
  const [groupID, setGroupID] = useState(groups[0]?.id ?? "");
  const [picked, setPicked] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (q ? resources.filter((r) => r.display_name.toLowerCase().includes(q)) : resources).slice(0, 40);
  }, [resources, search]);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      await api.createSLATarget({
        display_name: name.trim(),
        target_pct: pct,
        period,
        resource_ids: scopeKind === "resources" ? picked : [],
        group_ids: scopeKind === "group" && groupID ? [groupID] : [],
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [name, pct, period, scopeKind, picked, groupID, onCreated]);

  const scopeReady = scopeKind === "group" ? groupID !== "" : picked.length > 0;

  return (
    <Card title="New availability target">
      <div className="space-y-4 px-3 pt-2 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Production databases"
              className="w-64 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Measured over</span>
            <Select value={period} onChange={setPeriod} options={PERIODS} label="Period" />
          </label>
        </div>

        <div>
          <div className="mb-1.5 text-[12px] text-slate-500">Target</div>
          <div className="flex flex-wrap items-center gap-1.5">
            {PRESETS.map((p) => (
              <Button
                key={p.pct}
                size="xs"
                variant={pct === p.pct ? "primary" : "default"}
                onClick={() => setPct(p.pct)}
              >
                {p.label}
              </Button>
            ))}
            <input
              type="number"
              step="0.01"
              min={50}
              max={99.999}
              value={pct}
              onChange={(e) => setPct(Number(e.target.value))}
              aria-label="Custom target percentage"
              className="w-24 rounded border border-slate-300 px-2 py-1 text-right text-[13px] tabular-nums"
            />
          </div>
          <p className="mt-1.5 text-[12px] text-slate-600">
            Allows <span className="font-medium">{duration(allowedPerDay(pct))}</span> of downtime a
            day, or <span className="font-medium">{duration(allowedPerDay(pct) * 30)}</span> a month.
          </p>
        </div>

        <div className="space-y-2">
          <div className="flex items-center gap-3">
            <span className="text-[12px] text-slate-500">Applies to</span>
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
                  No groups yet —{" "}
                  <Link to="/groups" className="text-brand-600 hover:underline">
                    create one
                  </Link>{" "}
                  or pick monitors directly.
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
              </div>
              <div className="max-h-48 overflow-y-auto rounded border border-slate-200">
                {visible.map((r) => {
                  const on = picked.includes(r.id);
                  return (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() => setPicked(on ? picked.filter((x) => x !== r.id) : [...picked, r.id])}
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
                      <span className="ml-auto shrink-0 text-[11px] text-slate-400">{r.type_name}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          )}
        </div>

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
            {busy ? "Creating…" : "Create target"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

export function SLO() {
  const { user } = useAuth();
  const canEdit = user.role !== "viewer";
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);

  const targets = useAsync(() => api.slaTargets(), []);
  const monitors = useAsync(() => api.resources({ page_size: 500 }), []);
  const groups = useAsync(() => api.monitorGroups(), []);

  const remove = useCallback(
    async (id: string) => {
      setBusyID(id);
      setError(null);
      try {
        await api.deleteSLATarget(id);
        targets.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyID(null);
      }
    },
    [targets],
  );

  const d = targets.data;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="SLO"
        meta={d ? `${d.count} ${d.count === 1 ? "target" : "targets"}` : undefined}
        actions={
          canEdit && !adding ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              New target
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {adding && (
          <NewTarget
            resources={monitors.data?.items ?? []}
            groups={groups.data?.items ?? []}
            onCreated={() => {
              setAdding(false);
              targets.reload();
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

        {targets.initialLoading ? (
          <Spinner label="Loading targets" />
        ) : targets.error ? (
          <ErrorState error={targets.error} onRetry={targets.reload} />
        ) : !d ? null : (
          <>
            <Card title="Availability targets">
              {d.targets.length === 0 ? (
                <EmptyState
                  title="No targets defined"
                  hint="A target pairs a percentage with a set of monitors. Until one exists the SLA report has nothing to measure against, which is why it appears empty."
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse">
                    <thead>
                      <tr className="bg-slate-50">
                        {["Target", "Commitment", "Allows per day", "Period", "Monitors", ""].map(
                          (h, i) => (
                            <th
                              key={h || i}
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
                      {d.targets.map((t: SLATarget, i) => (
                        <tr key={t.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                          <td className="px-3 py-2 text-[13px] font-medium text-slate-800">
                            {t.display_name}
                          </td>
                          <td className="px-3 py-2 text-[13px] tabular-nums text-slate-700">
                            {t.target_pct}%
                          </td>
                          <td className="px-3 py-2 text-[13px] text-slate-600">
                            {duration(t.allowed_down_sec_per_day)}
                          </td>
                          <td className="px-3 py-2 text-[13px] text-slate-600">{t.period}</td>
                          <td className="px-3 py-2 text-[13px]">
                            <span
                              className={cx(
                                "font-medium",
                                t.resource_count === 0 ? "text-st-down" : "text-slate-800",
                              )}
                            >
                              {t.resource_count}
                            </span>
                          </td>
                          <td className="px-3 py-2 text-right">
                            {canEdit && (
                              <Button
                                size="xs"
                                variant="ghost"
                                disabled={busyID === t.id}
                                onClick={() => remove(t.id)}
                              >
                                <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                                <span className="sr-only">Delete {t.display_name}</span>
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

            {d.targets.length > 0 && (
              <InfoBanner>
                Compliance is measured on the{" "}
                <Link to="/reports?tab=sla" className="font-medium text-brand-600 hover:underline">
                  SLA report
                </Link>
                , which compares each target against recorded availability and shows the remaining
                error budget.
              </InfoBanner>
            )}

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              A 100% target is refused: any single failed check breaches it, so it would report a
              permanent breach and teach everyone to ignore the report. The{" "}
              <span className="font-medium">allows per day</span> column is the same commitment in
              time, which is the form a person can actually judge.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

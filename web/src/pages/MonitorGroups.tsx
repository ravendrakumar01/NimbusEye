/**
 * Monitor Groups.
 *
 * A group is a named set of monitors with a health state derived from its members.
 * It earns its place by being the unit other things point at: a maintenance window
 * or an SLA target scoped to "Production" keeps working as machines come and go,
 * where a list of individual monitors rots.
 *
 * The health strategy is explicit rather than assumed. "Worst child" is right for a
 * set of singletons where any failure matters; a count or percentage is right for a
 * pool where losing one of twelve web servers is not an incident. Guessing that for
 * the operator produces either noise or silence.
 */

import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, ChevronDown, ChevronRight, Plus, Trash2 } from "lucide-react";

import { api } from "../lib/api";
import type { MonitorGroup, Resource } from "../lib/api";
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
  StatusDot,
} from "../components/ui";
import type { Status } from "../lib/api";

const STRATEGIES = [
  { value: "worst_child", label: "Worst member — any failure fails the group" },
  { value: "count", label: "Count — fails when N members are down" },
  { value: "percentage", label: "Percentage — fails when N% of members are down" },
];

function MemberPicker({
  resources,
  picked,
  onChange,
}: {
  resources: Resource[];
  picked: string[];
  onChange: (ids: string[]) => void;
}) {
  const [search, setSearch] = useState("");
  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (q ? resources.filter((r) => r.display_name.toLowerCase().includes(q)) : resources).slice(0, 50);
  }, [resources, search]);

  return (
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
          <Button size="xs" variant="ghost" onClick={() => onChange([])}>
            Clear
          </Button>
        )}
      </div>
      <div className="max-h-56 overflow-y-auto rounded border border-slate-200">
        {visible.length === 0 ? (
          <p className="p-3 text-[12px] text-slate-500">No monitors match.</p>
        ) : (
          visible.map((r) => {
            const on = picked.includes(r.id);
            return (
              <button
                key={r.id}
                type="button"
                onClick={() => onChange(on ? picked.filter((x) => x !== r.id) : [...picked, r.id])}
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
                <StatusDot status={r.status} />
                <span className="truncate">{r.display_name}</span>
                <span className="ml-auto shrink-0 text-[11px] text-slate-400">{r.type_name}</span>
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}

function NewGroup({
  resources,
  onCreated,
  onCancel,
}: {
  resources: Resource[];
  onCreated: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [strategy, setStrategy] = useState("worst_child");
  const [threshold, setThreshold] = useState(1);
  const [picked, setPicked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      await api.createGroup({
        display_name: name.trim(),
        description: description.trim(),
        health_strategy: strategy,
        health_threshold: strategy === "worst_child" ? null : threshold,
        resource_ids: picked,
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [name, description, strategy, threshold, picked, onCreated]);

  return (
    <Card title="New monitor group">
      <div className="space-y-4 px-3 pt-2 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Production"
              className="w-56 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Description</span>
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Customer facing services"
              className="w-72 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
        </div>

        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Group is unhealthy when</span>
            <Select value={strategy} onChange={setStrategy} options={STRATEGIES} label="Health strategy" />
          </label>
          {strategy !== "worst_child" && (
            <label className="flex flex-col gap-1">
              <span className="text-[12px] text-slate-500">
                {strategy === "percentage" ? "Percent down" : "Members down"}
              </span>
              <input
                type="number"
                min={1}
                max={strategy === "percentage" ? 100 : 999}
                value={threshold}
                onChange={(e) => setThreshold(Number(e.target.value))}
                className="w-24 rounded border border-slate-300 px-2 py-1 text-right text-[13px] tabular-nums"
              />
            </label>
          )}
        </div>

        <div>
          <div className="mb-1.5 text-[12px] text-slate-500">Members</div>
          <MemberPicker resources={resources} picked={picked} onChange={setPicked} />
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
          <Button variant="primary" disabled={busy || !name.trim()} onClick={submit}>
            {busy ? "Creating…" : "Create group"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

function GroupMembers({ id }: { id: string }) {
  const state = useAsync(() => api.groupMembers(id), [id]);
  if (state.initialLoading) return <Spinner label="Loading members" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  const members = state.data?.members ?? [];
  if (members.length === 0) {
    return (
      <p className="px-3 py-3 text-[12px] text-slate-500">
        No members. An empty group makes anything scoped to it a no-op, so add monitors or delete it.
      </p>
    );
  }
  return (
    <ul className="divide-y divide-slate-100">
      {members.map((m) => (
        <li key={m.id} className="flex items-center gap-2 px-3 py-1.5 text-[13px]">
          <StatusDot status={m.status} />
          <Link to={`/monitor/${m.id}`} className="text-brand-600 hover:underline">
            {m.display_name}
          </Link>
          <span className="ml-auto text-[11px] text-slate-400">{m.status}</span>
        </li>
      ))}
    </ul>
  );
}

export function MonitorGroups() {
  const { user } = useAuth();
  const canEdit = user.role !== "viewer";
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);

  const groups = useAsync(() => api.monitorGroups(), []);
  const monitors = useAsync(() => api.resources({ page_size: 500 }), []);

  const remove = useCallback(
    async (id: string) => {
      setBusyID(id);
      setError(null);
      try {
        await api.deleteGroup(id);
        groups.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyID(null);
      }
    },
    [groups],
  );

  const items = groups.data?.items ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Monitor Groups"
        meta={groups.data ? `${items.length} ${items.length === 1 ? "group" : "groups"}` : undefined}
        actions={
          canEdit && !adding ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              New group
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {adding && (
          <NewGroup
            resources={monitors.data?.items ?? []}
            onCreated={() => {
              setAdding(false);
              groups.reload();
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

        {groups.initialLoading ? (
          <Spinner label="Loading groups" />
        ) : groups.error ? (
          <ErrorState error={groups.error} onRetry={groups.reload} />
        ) : items.length === 0 ? (
          <Card>
            <EmptyState
              title="No monitor groups"
              hint="Group monitors that fail together or belong to one service. Maintenance windows and SLA targets can then point at the group, so they keep working as machines are added and removed."
            />
          </Card>
        ) : (
          <Card title="Groups">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    {["Group", "Health", "Members", "Up", "Down", "Trouble", "Unknown", ""].map(
                      (h, i) => (
                        <th
                          key={h || i}
                          scope="col"
                          className={cx(
                            "border-b border-slate-200 px-3 py-2 text-[12px] font-medium text-slate-600",
                            i >= 2 && i <= 6 ? "text-right" : "text-left",
                          )}
                        >
                          {h}
                        </th>
                      ),
                    )}
                  </tr>
                </thead>
                <tbody>
                  {items.map((g: MonitorGroup, i) => (
                    <>
                      <tr key={g.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                        <td className="px-3 py-2">
                          <button
                            type="button"
                            onClick={() => setOpen(open === g.id ? null : g.id)}
                            className="flex items-center gap-1 text-left text-[13px] text-brand-600 hover:underline"
                          >
                            {open === g.id ? (
                              <ChevronDown className="size-3.5" aria-hidden="true" />
                            ) : (
                              <ChevronRight className="size-3.5" aria-hidden="true" />
                            )}
                            {g.display_name}
                          </button>
                          {g.description && (
                            <div className="pl-5 text-[11px] text-slate-400">{g.description}</div>
                          )}
                        </td>
                        <td className="px-3 py-2">
                          <span className="inline-flex items-center gap-1.5 text-[13px]">
                            <StatusDot status={g.health as Status} />
                            {g.health}
                          </span>
                          <div
                            className="text-[11px] text-slate-400"
                            title="How the group's state is derived from its members"
                          >
                            {g.health_strategy === "worst_child"
                              ? "worst member"
                              : g.health_strategy === "percentage"
                                ? `${g.health_threshold}% down`
                                : `${g.health_threshold} down`}
                          </div>
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                          <span className={g.member_count === 0 ? "text-st-down" : "text-slate-800"}>
                            {g.member_count}
                          </span>
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-600">
                          {g.up || <span className="text-slate-300">—</span>}
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                          {g.down ? (
                            <span className="font-medium text-st-down">{g.down}</span>
                          ) : (
                            <span className="text-slate-300">—</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                          {g.trouble ? (
                            <span className="text-st-trouble">{g.trouble}</span>
                          ) : (
                            <span className="text-slate-300">—</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-500">
                          {g.unknown || <span className="text-slate-300">—</span>}
                        </td>
                        <td className="px-3 py-2 text-right">
                          {canEdit && (
                            <Button
                              size="xs"
                              variant="ghost"
                              disabled={busyID === g.id}
                              onClick={() => remove(g.id)}
                            >
                              <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                              <span className="sr-only">Delete {g.display_name}</span>
                            </Button>
                          )}
                        </td>
                      </tr>
                      {open === g.id && (
                        <tr key={`${g.id}-members`}>
                          <td colSpan={8} className="border-y border-slate-200 bg-white p-0">
                            <GroupMembers id={g.id} />
                          </td>
                        </tr>
                      )}
                    </>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          A group whose members are all unknown or suspended reports{" "}
          <span className="font-medium">unknown</span>, not up. Claiming a group is healthy when
          nothing in it has been measured would be the same mistake as scoring an unmeasured day as
          100% available. Deleting a group is refused while a maintenance window or SLA target points
          at it.
        </p>
      </div>
    </div>
  );
}

/**
 * Audit log.
 *
 * Append-only, and there is no endpoint that edits or deletes an entry — the
 * application role has UPDATE and DELETE revoked on the table, so even a
 * compromised session cannot erase its own tracks.
 *
 * Paging uses a cursor on the entry id rather than an offset. The log grows while
 * it is being read, and an offset would quietly skip or repeat rows as it does.
 */

import { useCallback, useState } from "react";
import { ChevronDown } from "lucide-react";

import { api } from "../lib/api";
import type { AuditEntry } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { absolute, cx, since } from "../lib/format";
import {
  Card,
  Button,
  EmptyState,
  ErrorState,
  PageHeader,
  Select,
  Spinner,
} from "../components/ui";

/** Actions are grouped by what they touch, so colour carries meaning. */
function actionTone(action: string): string {
  if (action.startsWith("login.failure") || action.endsWith(".delete")) return "text-st-down";
  if (action.startsWith("login") || action === "logout") return "text-slate-500";
  if (action.endsWith(".create") || action.endsWith(".invite")) return "text-st-up";
  return "text-brand-600";
}

/** Renders the detail object as readable key/value text rather than raw JSON. */
function Detail({ detail }: { detail: Record<string, unknown> }) {
  const entries = Object.entries(detail).filter(([, v]) => v !== null && v !== "");
  if (entries.length === 0) return <span className="text-slate-300">—</span>;
  return (
    <span className="text-[12px] text-slate-600">
      {entries.map(([k, v], i) => (
        <span key={k}>
          {i > 0 && <span className="text-slate-300"> · </span>}
          <span className="text-slate-400">{k.replace(/_/g, " ")}</span> {String(v)}
        </span>
      ))}
    </span>
  );
}

export function AdminAudit() {
  const [action, setAction] = useState("");
  const [pages, setPages] = useState<number[]>([0]);
  const cursor = pages[pages.length - 1] ?? 0;

  const state = useAsync(
    () => api.auditLog({ action: action || undefined, before: cursor || undefined, limit: 50 }),
    [action, cursor],
  );

  const changeAction = useCallback((v: string) => {
    setAction(v);
    // A new filter starts a new sequence; keeping the old cursor would land in the
    // middle of a different result set.
    setPages([0]);
  }, []);

  const entries = state.data?.entries ?? [];
  const actions = state.data?.actions ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Audit Log"
        meta={pages.length > 1 ? `page ${pages.length}` : undefined}
        actions={
          <Select
            value={action}
            onChange={changeAction}
            options={[{ value: "", label: "All actions" }, ...actions.map((a) => ({ value: a, label: a }))]}
            label="Action"
          />
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.initialLoading ? (
          <Spinner label="Loading the log" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : entries.length === 0 ? (
          <Card>
            <EmptyState
              title="Nothing recorded for this filter"
              hint={action ? "Try All actions." : "Actions appear here as they happen."}
            />
          </Card>
        ) : (
          <Card title="Recorded actions, most recent first">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    {["When", "Action", "By", "Object", "Detail", "From"].map((h) => (
                      <th
                        key={h}
                        scope="col"
                        className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                      >
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {entries.map((e: AuditEntry, i) => (
                    <tr key={e.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                      <td className="px-3 py-2 text-[13px] whitespace-nowrap text-slate-600" title={absolute(e.created_at)}>
                        {since(e.created_at)}
                      </td>
                      <td className="px-3 py-2">
                        <span className={cx("font-mono text-[12px]", actionTone(e.action))}>{e.action}</span>
                      </td>
                      <td className="px-3 py-2 text-[13px] text-slate-700">
                        {e.user_email ?? (
                          <span className="text-slate-400" title="The user record has since been deleted">
                            deleted user
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-[12px] text-slate-500">
                        {e.object_type ? e.object_type.replace(/_/g, " ") : <span className="text-slate-300">—</span>}
                      </td>
                      <td className="px-3 py-2">
                        <Detail detail={e.detail} />
                      </td>
                      <td className="px-3 py-2 font-mono text-[11px] text-slate-400">
                        {e.ip ?? "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div className="flex items-center gap-2 border-t border-slate-200 px-3 py-2.5">
              {pages.length > 1 && (
                <Button size="xs" onClick={() => setPages(pages.slice(0, -1))}>
                  Previous
                </Button>
              )}
              {state.data && state.data.next_before > 0 && (
                <Button size="xs" onClick={() => setPages([...pages, state.data!.next_before])}>
                  <ChevronDown className="size-3.5" aria-hidden="true" />
                  Older entries
                </Button>
              )}
              {state.data && state.data.next_before === 0 && (
                <span className="text-[11px] text-slate-400">end of the log</span>
              )}
            </div>
          </Card>
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          The log is append-only: the application's database role has UPDATE and DELETE revoked on
          this table, so a session that is compromised still cannot erase what it did. Entries are
          kept indefinitely.
        </p>
      </div>
    </div>
  );
}

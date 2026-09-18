/**
 * Notification channels.
 *
 * A channel is where an alert is delivered. The rule that shapes this whole screen
 * is that the console never holds a credential: a Slack webhook URL, a PagerDuty
 * routing key and an SMTP password are all secrets, and a secret pasted into a form
 * ends up in the database, in backups, and in this page's own network log.
 *
 * So for those channel types the form asks for a *path on the server* instead. It
 * is more friction than a paste box, and it is the reason a database dump of this
 * product contains nothing worth stealing.
 */

import { useCallback, useState } from "react";
import { AlertTriangle, Check, Plus, Trash2 } from "lucide-react";

import { api } from "../lib/api";
import type { NotificationChannel } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { cx, since } from "../lib/format";
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

/** Channel types, with what each one needs and why. */
const TYPES = [
  { value: "email", label: "Email", needs: "address", secret: false },
  { value: "slack", label: "Slack", needs: "", secret: true },
  { value: "teams", label: "Microsoft Teams", needs: "", secret: true },
  { value: "webhook", label: "Webhook", needs: "", secret: true },
  { value: "pagerduty", label: "PagerDuty", needs: "", secret: true },
  { value: "sms", label: "SMS", needs: "phone", secret: false },
];

function typeInfo(t: string) {
  return TYPES.find((x) => x.value === t);
}

function NewChannel({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
  const [type, setType] = useState("email");
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [phone, setPhone] = useState("");
  const [secretRef, setSecretRef] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const info = typeInfo(type);
  const needsSecret = info?.secret ?? false;

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    const config: Record<string, unknown> = {};
    if (type === "email") config.address = address.trim();
    if (type === "sms") config.phone = phone.trim();
    try {
      await api.createChannel({
        channel_type: type,
        display_name: name.trim(),
        config,
        secret_ref: secretRef.trim(),
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [type, name, address, phone, secretRef, onCreated]);

  return (
    <Card title="New channel">
      <div className="space-y-3 px-3 pt-1 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Type</span>
            <Select value={type} onChange={setType} options={TYPES} label="Channel type" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Ops on-call"
              className="w-56 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
        </div>

        {type === "email" && (
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Address</span>
            <input
              type="email"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              placeholder="ops@example.com"
              className="w-72 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
        )}

        {type === "sms" && (
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Phone number</span>
            <input
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="+91…"
              className="w-56 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
        )}

        {needsSecret && (
          <div className="space-y-1.5">
            <label className="flex flex-col gap-1">
              <span className="text-[12px] text-slate-500">
                Path to the file holding the URL or token
              </span>
              <input
                value={secretRef}
                onChange={(e) => setSecretRef(e.target.value)}
                placeholder="/etc/nimbuseye/creds/slack.secret"
                className="w-full max-w-xl rounded border border-slate-300 px-2 py-1 font-mono text-[13px]"
              />
            </label>
            <InfoBanner>
              Paste the path, not the URL. A {info?.label} webhook URL is itself the credential —
              anyone holding it can post as you. Write it to a file readable only by the service
              account and reference it here, and it stays out of the database, out of backups and out
              of this page.
            </InfoBanner>
          </div>
        )}

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
            {busy ? "Creating…" : "Create channel"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

export function AdminChannels() {
  const { user } = useAuth();
  const canEdit = user.role === "owner" || user.role === "admin";
  const [adding, setAdding] = useState(false);
  const [busyID, setBusyID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const state = useAsync(() => api.channels(), []);

  const act = useCallback(
    async (id: string, fn: () => Promise<unknown>) => {
      setBusyID(id);
      setError(null);
      try {
        await fn();
        state.reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyID(null);
      }
    },
    [state],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Notification Channels"
        meta={state.data ? `${state.data.channels.length} configured` : undefined}
        actions={
          canEdit && !adding ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              Add channel
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.data && !state.data.smtp_configured && (
          <InfoBanner tone="warn">
            This server has no SMTP relay configured, so email channels cannot deliver anything. Set
            the SMTP settings in the service environment first — a channel that looks healthy and
            sends nothing is worse than no channel at all.
          </InfoBanner>
        )}

        {adding && (
          <NewChannel
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
          <Spinner label="Loading channels" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : !state.data || state.data.channels.length === 0 ? (
          <Card>
            <EmptyState
              title="No channels yet"
              hint="Until one exists, alerts are recorded and shown on screen but nobody is told about them."
            />
          </Card>
        ) : (
          <Card title="Where alerts are delivered">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    {["Channel", "Type", "Destination", "Used by", "State", ""].map((h, i) => (
                      <th
                        key={h || i}
                        scope="col"
                        className={cx(
                          "border-b border-slate-200 px-3 py-2 text-[12px] font-medium text-slate-600",
                          i === 3 ? "text-right" : "text-left",
                        )}
                      >
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {state.data.channels.map((c: NotificationChannel, i) => {
                    const dest =
                      (c.config.address as string) ??
                      (c.config.phone as string) ??
                      (c.config.channel as string) ??
                      "";
                    return (
                      <tr key={c.id} className={i % 2 ? "bg-slate-50/60" : undefined}>
                        <td className="px-3 py-2">
                          <div className="text-[13px] font-medium text-slate-800">{c.display_name}</div>
                          <div className="text-[11px] text-slate-400">added {since(c.created_at)}</div>
                        </td>
                        <td className="px-3 py-2 text-[13px] text-slate-600">
                          {typeInfo(c.channel_type)?.label ?? c.channel_type}
                        </td>
                        <td className="px-3 py-2 text-[13px] text-slate-600">
                          {dest ? (
                            dest
                          ) : c.secret_ref ? (
                            <span className="font-mono text-[11px] text-slate-500" title="Server-side file holding the credential">
                              {c.secret_ref}
                            </span>
                          ) : (
                            <span className="text-slate-300">—</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                          {c.used_by > 0 ? (
                            <span className="text-slate-700">{c.used_by}</span>
                          ) : (
                            <span
                              className="text-st-trouble"
                              title="No notification profile routes to this channel, so it will never be used"
                            >
                              unused
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2">
                          {!c.enabled ? (
                            <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500">
                              disabled
                            </span>
                          ) : c.verified_at ? (
                            <span className="inline-flex items-center gap-1 text-[11px] text-st-up">
                              <Check className="size-3" aria-hidden="true" />
                              verified {since(c.verified_at)}
                            </span>
                          ) : (
                            <span className="text-[11px] text-slate-400" title="Nothing has been delivered through this channel yet">
                              not yet verified
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right whitespace-nowrap">
                          {canEdit && (
                            <>
                              <Button
                                size="xs"
                                disabled={busyID === c.id}
                                onClick={() => act(c.id, () => api.updateChannel(c.id, { enabled: !c.enabled }))}
                              >
                                {c.enabled ? "Disable" : "Enable"}
                              </Button>
                              <Button
                                size="xs"
                                variant="ghost"
                                disabled={busyID === c.id}
                                onClick={() => act(c.id, () => api.deleteChannel(c.id))}
                              >
                                <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                                <span className="sr-only">Delete {c.display_name}</span>
                              </Button>
                            </>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </Card>
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          Slack, Teams, webhook and PagerDuty channels hold their credential in a file on the server
          and this console stores only the path. A channel marked{" "}
          <span className="font-medium">unused</span> is configured but no notification profile
          routes to it, so it will never fire.
        </p>
      </div>
    </div>
  );
}

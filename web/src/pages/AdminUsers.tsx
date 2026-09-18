/**
 * Users.
 *
 * An administrator invites someone; they set their own password through a one-time
 * link. There is deliberately no field here for setting another person's password:
 * a password an admin knows is one that has been sent over some channel in plain
 * text, and it invariably ends up in a chat message that is never deleted.
 *
 * The server refuses self-demotion, self-disable, self-delete and removing the last
 * owner. Those buttons are hidden here too, but hiding them is the courtesy — the
 * refusal is what actually protects the tenant.
 */

import { useCallback, useState } from "react";
import { AlertTriangle, KeyRound, Mail, Plus, Trash2, Unlock } from "lucide-react";

import { api } from "../lib/api";
import type { AdminUser } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { absolute, cx, since } from "../lib/format";
import {
  Button,
  Card,
  ErrorState,
  InfoBanner,
  PageHeader,
  Select,
  Spinner,
} from "../components/ui";

const ROLES = [
  { value: "owner", label: "Owner — full control, cannot be removed by others" },
  { value: "admin", label: "Admin — can change configuration and users" },
  { value: "operator", label: "Operator — can acknowledge alerts" },
  { value: "viewer", label: "Viewer — read only" },
];

const ROLE_SHORT: Record<string, string> = {
  owner: "Owner",
  admin: "Admin",
  operator: "Operator",
  viewer: "Viewer",
};

function StatusChip({ user }: { user: AdminUser }) {
  const locked = user.locked_until && Date.parse(user.locked_until) > Date.now();
  if (locked) {
    return (
      <span
        className="rounded bg-st-critical-bg px-1.5 py-0.5 text-[11px] font-medium text-st-critical"
        title={`Locked until ${absolute(user.locked_until)} after ${user.failed_logins} failed attempts`}
      >
        locked
      </span>
    );
  }
  if (user.status === "invited") {
    return (
      <span
        className="rounded bg-st-trouble-bg px-1.5 py-0.5 text-[11px] font-medium text-st-trouble"
        title="Invited but has not set a password yet"
      >
        invited
      </span>
    );
  }
  if (user.status === "disabled") {
    return <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500">disabled</span>;
  }
  return <span className="rounded bg-st-up-bg px-1.5 py-0.5 text-[11px] font-medium text-st-up">active</span>;
}

function InviteForm({ onDone, onCancel }: { onDone: (msg: string) => void; onCancel: () => void }) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState("viewer");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.createAdminUser({
        email: email.trim(),
        display_name: name.trim(),
        role,
      });
      onDone(res.next_action);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [email, name, role, onDone]);

  return (
    <Card title="Invite someone">
      <div className="space-y-3 px-3 pt-1 pb-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Email</span>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="person@example.com"
              className="w-64 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Full name"
              className="w-56 rounded border border-slate-300 px-2 py-1 text-[13px]"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500">Role</span>
            <Select value={role} onChange={setRole} options={ROLES} label="Role" />
          </label>
        </div>

        <InfoBanner>
          They receive a one-time link and choose their own password. No password is set here —
          nobody, including you, should know someone else's.
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
          <Button variant="primary" disabled={busy || !email.trim() || !name.trim()} onClick={submit}>
            {busy ? "Inviting…" : "Send invitation"}
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

export function AdminUsers() {
  const { user: me } = useAuth();
  const canEdit = me.role === "owner" || me.role === "admin";
  const [inviting, setInviting] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);
  const state = useAsync(() => api.adminUsers(), []);

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

  const users = state.data?.users ?? [];
  const owners = users.filter((u) => u.role === "owner" && u.status !== "disabled").length;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Users"
        meta={state.data ? `${users.length} ${users.length === 1 ? "user" : "users"}` : undefined}
        actions={
          canEdit && !inviting ? (
            <Button variant="primary" onClick={() => setInviting(true)}>
              <Plus className="size-3.5" aria-hidden="true" />
              Invite user
            </Button>
          ) : undefined
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {state.data && !state.data.smtp_configured && (
          <InfoBanner tone="warn">
            No SMTP relay is configured, so invitation and reset emails cannot be sent. An invited
            user would have no way to set a password.
          </InfoBanner>
        )}

        {notice && <InfoBanner>{notice}</InfoBanner>}

        {inviting && (
          <InviteForm
            onDone={(msg) => {
              setInviting(false);
              setNotice(msg);
              state.reload();
            }}
            onCancel={() => setInviting(false)}
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
          <Spinner label="Loading users" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : (
          <Card title="Who can sign in">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    {["User", "Role", "State", "Last signed in", "MFA", ""].map((h, i) => (
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
                  {users.map((u: AdminUser, i) => {
                    const isMe = u.id === state.data?.me;
                    const lastOwner = u.role === "owner" && owners <= 1;
                    const locked = u.locked_until && Date.parse(u.locked_until) > Date.now();
                    return (
                      <tr key={u.id} className={cx(i % 2 ? "bg-slate-50/60" : undefined, isMe && "bg-brand-50/50")}>
                        <td className="px-3 py-2">
                          <div className="text-[13px] font-medium text-slate-800">
                            {u.display_name}
                            {isMe && <span className="ml-1.5 text-[11px] font-normal text-slate-400">(you)</span>}
                          </div>
                          <div className="text-[11px] text-slate-500">{u.email}</div>
                        </td>
                        <td className="px-3 py-2">
                          {canEdit && !isMe && !lastOwner ? (
                            <Select
                              value={u.role}
                              onChange={(role) => act(u.id, () => api.updateAdminUser(u.id, { role }))}
                              options={Object.entries(ROLE_SHORT).map(([v, l]) => ({ value: v, label: l }))}
                              label={`Role for ${u.display_name}`}
                            />
                          ) : (
                            <span className="text-[13px] text-slate-700" title={lastOwner ? "The only owner cannot be demoted" : undefined}>
                              {ROLE_SHORT[u.role] ?? u.role}
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2">
                          <StatusChip user={u} />
                          {!u.has_password && u.status !== "disabled" && (
                            <div className="mt-0.5 flex items-center gap-1 text-[11px] text-slate-400">
                              <KeyRound className="size-3" aria-hidden="true" />
                              no password set
                            </div>
                          )}
                        </td>
                        <td className="px-3 py-2 text-[13px] text-slate-600">
                          {u.last_login_at ? (
                            <span title={absolute(u.last_login_at)}>{since(u.last_login_at)}</span>
                          ) : (
                            <span className="text-slate-300">never</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-[13px]">
                          {u.mfa_enabled ? (
                            <span className="text-st-up">on</span>
                          ) : (
                            <span className="text-slate-400" title="Multi-factor authentication is not implemented yet">
                              off
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right whitespace-nowrap">
                          {canEdit && (
                            <>
                              {locked && (
                                <Button size="xs" disabled={busyID === u.id} onClick={() => act(u.id, () => api.unlockAdminUser(u.id))}>
                                  <Unlock className="size-3.5" aria-hidden="true" />
                                  Unlock
                                </Button>
                              )}
                              {!u.has_password && (
                                <Button
                                  size="xs"
                                  disabled={busyID === u.id}
                                  title="Send another link to set a password"
                                  onClick={() => act(u.id, async () => {
                                    await api.forgotPassword(u.email);
                                    setNotice(`A password link has been sent to ${u.email}.`);
                                  })}
                                >
                                  <Mail className="size-3.5" aria-hidden="true" />
                                  Resend
                                </Button>
                              )}
                              {!isMe && !lastOwner && u.status !== "disabled" && (
                                <Button size="xs" disabled={busyID === u.id} onClick={() => act(u.id, () => api.updateAdminUser(u.id, { status: "disabled" }))}>
                                  Disable
                                </Button>
                              )}
                              {!isMe && u.status === "disabled" && u.has_password && (
                                <Button size="xs" disabled={busyID === u.id} onClick={() => act(u.id, () => api.updateAdminUser(u.id, { status: "active" }))}>
                                  Enable
                                </Button>
                              )}
                              {!isMe && !lastOwner && (
                                <Button size="xs" variant="ghost" disabled={busyID === u.id} onClick={() => act(u.id, () => api.deleteAdminUser(u.id))}>
                                  <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                                  <span className="sr-only">Delete {u.display_name}</span>
                                </Button>
                              )}
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
          Disabling a user ends their signed-in sessions immediately, not just their next login. The
          only remaining owner cannot be demoted, disabled or deleted — otherwise the tenant can be
          left with nobody able to grant access back, and the only repair is direct database access.
        </p>
      </div>
    </div>
  );
}

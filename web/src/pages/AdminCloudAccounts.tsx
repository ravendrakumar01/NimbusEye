/**
 * Admin > Cloud Accounts.
 *
 * Two things this page is careful about:
 *
 * 1. Discovery health is the headline, not an afterthought. An account that has
 *    stopped discovering makes every count elsewhere in the product wrong, so a
 *    degraded account is loud here.
 * 2. The private key never enters the browser. The form collects a server-side
 *    file path for it; tenancy/user/fingerprint values are configuration, not
 *    secrets, and those are edited normally.
 *
 * The form fields are generated from the API's provider spec, so adding a
 * provider is a backend change only.
 */

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  CircleCheck,
  Clock,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
  TriangleAlert,
  X,
} from "lucide-react";
import { ApiError, api } from "../lib/api";
import type { AccountInput, CloudAccount, ProviderSpec, VerifyResult } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { PROVIDER_LABEL, absolute, cx, num, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
} from "../components/ui";

export function AdminCloudAccounts() {
  const accounts = useAsync(() => api.accounts(), []);
  const specs = useAsync(() => api.providerSpecs(), []);

  const [editing, setEditing] = useState<CloudAccount | "new" | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [verifyResult, setVerifyResult] = useState<{ id: string; res: VerifyResult } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<CloudAccount | null>(null);
  const [notice, setNotice] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  async function verify(id: string) {
    setVerifying(id);
    setVerifyResult(null);
    try {
      const res = await api.verifyAccount(id);
      setVerifyResult({ id, res });
    } catch (e) {
      setNotice({ kind: "err", text: e instanceof Error ? e.message : "Verification failed" });
    } finally {
      setVerifying(null);
    }
  }

  async function remove(a: CloudAccount) {
    try {
      const res = await api.deleteAccount(a.id);
      setNotice({
        kind: "ok",
        text: `${a.display_name} removed. ${num(res.resources_affected)} resources will stop being monitored.`,
      });
      setConfirmDelete(null);
      accounts.reload();
    } catch (e) {
      setNotice({ kind: "err", text: e instanceof Error ? e.message : "Could not remove account" });
    }
  }

  return (
    <>
      <PageHeader
        title="Cloud Accounts"
        meta={accounts.data ? `${num(accounts.data.total)} connected` : undefined}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus className="size-3.5" aria-hidden="true" />
            Integrate Account
          </Button>
        }
      />

      <div className="space-y-4 px-5 py-4">
        {notice && (
          <div
            role="status"
            className={cx(
              "flex items-start gap-2 rounded border px-3 py-2 text-[13px]",
              notice.kind === "ok"
                ? "border-st-up/30 bg-st-up-bg text-slate-700"
                : "border-st-down/30 bg-st-down-bg text-slate-700",
            )}
          >
            <span className="flex-1">{notice.text}</span>
            <button type="button" onClick={() => setNotice(null)} aria-label="Dismiss">
              <X className="size-3.5 text-slate-400" aria-hidden="true" />
            </button>
          </div>
        )}

        {accounts.initialLoading ? (
          <Spinner label="Loading accounts" />
        ) : accounts.error ? (
          <ErrorState error={accounts.error} onRetry={accounts.reload} />
        ) : !accounts.data?.items.length ? (
          <Card className="py-6">
            <EmptyState
              title="No cloud accounts connected"
              hint="Connect an account with read-only credentials to begin discovery."
            />
          </Card>
        ) : (
          <div className="space-y-3">
            {accounts.data.items.map((a) => (
              <AccountRow
                key={a.id}
                account={a}
                verifying={verifying === a.id}
                verifyResult={verifyResult?.id === a.id ? verifyResult.res : null}
                onVerify={() => verify(a.id)}
                onEdit={() => setEditing(a)}
                onDelete={() => setConfirmDelete(a)}
              />
            ))}
          </div>
        )}
      </div>

      {editing && specs.data && (
        <AccountForm
          specs={specs.data.items}
          account={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            setEditing(null);
            setNotice({ kind: "ok", text: msg });
            accounts.reload();
          }}
        />
      )}

      {confirmDelete && (
        <Modal title="Remove cloud account" onClose={() => setConfirmDelete(null)}>
          <p className="text-[13px] text-slate-700">
            Removing <strong>{confirmDelete.display_name}</strong> stops discovery and monitoring
            for its <strong>{num(confirmDelete.resource_count)}</strong> resources. Historical
            availability and cost data is retained.
          </p>
          <p className="mt-2 text-[13px] text-slate-500">
            The credential file on the server is not deleted; remove it separately if it is no
            longer needed.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button onClick={() => setConfirmDelete(null)}>Cancel</Button>
            <Button variant="danger" onClick={() => remove(confirmDelete)}>
              Remove account
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}

/* ------------------------------------------------------------------ row */

function AccountRow({
  account: a,
  verifying,
  verifyResult,
  onVerify,
  onEdit,
  onDelete,
}: {
  account: CloudAccount;
  verifying: boolean;
  verifyResult: VerifyResult | null;
  onVerify: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const degraded = a.discovery_state === "failed" || a.discovery_state === "partial";
  const running = a.discovery_state === "running";

  return (
    <Card className="px-4 py-3">
      <div className="flex flex-wrap items-start gap-4">
        <span
          className={cx(
            "grid size-9 shrink-0 place-items-center rounded text-[11px] font-bold uppercase",
            degraded ? "bg-st-critical-bg text-st-critical" : "bg-brand-50 text-brand-600",
          )}
          aria-hidden="true"
        >
          {a.provider === "oci" ? "OCI" : a.provider === "aws" ? "AWS" : a.provider.slice(0, 2)}
        </span>

        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[14px] font-medium text-slate-800">{a.display_name}</span>
            <DiscoveryBadge state={a.discovery_state} />
            {!a.enabled && (
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-[11px] text-slate-600">
                Disabled
              </span>
            )}
          </div>
          <div className="mt-1 font-mono text-[11px] break-all text-slate-500">
            {a.native_account_id}
          </div>
          <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-slate-500">
            <span>{PROVIDER_LABEL[a.provider] ?? a.provider}</span>
            <span>{a.regions.length} region{a.regions.length === 1 ? "" : "s"}</span>
            <span className="inline-flex items-center gap-1">
              <Clock className="size-3" aria-hidden="true" />
              discovery {since(a.last_discovery_at)} ago
            </span>
            {a.credentials_ref && (
              <span className="font-mono" title="Credential file on the server">
                {a.credentials_ref}
              </span>
            )}
          </div>
          {a.last_error && (
            <p className="mt-2 rounded border border-st-critical/30 bg-st-critical-bg px-2 py-1 text-[12px] text-slate-700">
              {a.last_error}
            </p>
          )}
        </div>

        <div className="shrink-0 text-right">
          <Link
            to={`/admin/cloud-accounts/${a.id}`}
            className="num block text-lg font-medium text-brand-500 hover:underline"
          >
            {num(a.resource_count)}
          </Link>
          <div className="text-[11px] text-slate-500">resources</div>
        </div>

        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <Button size="xs" onClick={onVerify} disabled={verifying}>
            <ShieldCheck className="size-3.5" aria-hidden="true" />
            {verifying ? "Checking…" : "Verify"}
          </Button>
          <Button size="xs" onClick={onEdit}>
            <Pencil className="size-3.5" aria-hidden="true" />
            Edit
          </Button>
          <Button size="xs" variant="danger" onClick={onDelete}>
            <Trash2 className="size-3.5" aria-hidden="true" />
          </Button>
        </div>
      </div>

      {running && (
        // The reference console shows a large in-progress ring for this state.
        // Worth keeping: it is the difference between "nothing to monitor" and
        // "we have not looked yet", which are easy to confuse.
        <div className="mt-3 flex items-center gap-3 rounded border border-amber-200 bg-amber-50 px-3 py-2">
          <span
            className="size-6 shrink-0 animate-spin rounded-full border-2 border-amber-300 border-t-transparent"
            aria-hidden="true"
          />
          <span className="text-[13px] text-slate-700">
            Resources are being discovered. This can take a while on a large tenancy.
          </span>
        </div>
      )}

      {verifyResult && (
        <div className="mt-3 rounded border border-slate-200">
          <div
            className={cx(
              "border-b border-slate-200 px-3 py-2 text-[13px] font-medium",
              verifyResult.ok ? "text-st-up" : "text-st-down",
            )}
          >
            {verifyResult.message}
          </div>
          <ul className="divide-y divide-slate-100">
            {verifyResult.checks.map((c) => (
              <li key={c.name} className="flex items-start gap-2 px-3 py-1.5 text-[12px]">
                <span className="mt-0.5 shrink-0">
                  {c.status === "ok" ? (
                    <CircleCheck className="size-3.5 text-st-up" aria-hidden="true" />
                  ) : c.status === "failed" ? (
                    <TriangleAlert className="size-3.5 text-st-down" aria-hidden="true" />
                  ) : (
                    <span className="block size-3.5 rounded-full border border-slate-300" />
                  )}
                </span>
                <span className="w-56 shrink-0 text-slate-700">{c.name}</span>
                <span
                  className={cx(
                    "min-w-0 flex-1 break-words",
                    c.status === "failed" ? "text-st-down" : "text-slate-500",
                  )}
                >
                  {c.detail}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  );
}

function DiscoveryBadge({ state }: { state: CloudAccount["discovery_state"] }) {
  const map: Record<CloudAccount["discovery_state"], { label: string; cls: string }> = {
    ok: { label: "Discovery healthy", cls: "bg-st-up-bg text-st-up ring-st-up/30" },
    running: { label: "Discovering", cls: "bg-amber-50 text-amber-700 ring-amber-300/50" },
    partial: { label: "Partial discovery", cls: "bg-st-critical-bg text-st-critical ring-st-critical/30" },
    failed: { label: "Discovery failed", cls: "bg-st-down-bg text-st-down ring-st-down/30" },
    never: { label: "Never discovered", cls: "bg-slate-100 text-slate-600 ring-slate-300/50" },
  };
  const m = map[state];
  return (
    <span className={cx("rounded-full px-2 py-0.5 text-[11px] font-medium ring-1", m.cls)}>
      {m.label}
    </span>
  );
}

/* ----------------------------------------------------------------- form */

function AccountForm({
  specs,
  account,
  onClose,
  onSaved,
}: {
  specs: ProviderSpec[];
  account: CloudAccount | null;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const editing = account !== null;
  const [provider, setProvider] = useState<string>(account?.provider ?? specs[0]?.provider ?? "oci");
  const spec = specs.find((s) => s.provider === provider) ?? specs[0]!;

  const [displayName, setDisplayName] = useState(account?.display_name ?? "");
  const [accountID, setAccountID] = useState(account?.native_account_id ?? "");
  const [regions, setRegions] = useState<string[]>(account?.regions ?? []);
  const [credRef, setCredRef] = useState(account?.credentials_ref ?? "");
  const [config, setConfig] = useState<Record<string, string>>(account?.config ?? {});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);

  // Switching provider clears provider-specific state; keeping stale OCIDs in an
  // AWS form would just produce confusing validation errors.
  useEffect(() => {
    if (editing) return;
    setConfig({});
    setRegions([]);
    setAccountID("");
    setErrors({});
  }, [provider, editing]);

  async function submit() {
    setSaving(true);
    setErrors({});
    setBanner(null);
    const input: AccountInput = {
      provider,
      display_name: displayName,
      native_account_id: accountID,
      regions,
      credentials_ref: credRef,
      config,
    };
    try {
      if (editing) {
        await api.updateAccount(account.id, input);
        onSaved(`${displayName} updated.`);
      } else {
        await api.createAccount(input);
        onSaved(`${displayName} connected. Discovery has been queued.`);
      }
    } catch (e) {
      if (e instanceof ApiError && e.fields) {
        setErrors(e.fields);
        setBanner(e.message);
      } else {
        setBanner(e instanceof Error ? e.message : "Could not save the account");
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title={editing ? `Edit ${account.display_name}` : "Integrate Cloud Account"}
      onClose={onClose}
      wide
    >
      <div className="space-y-4">
        {banner && (
          <div className="rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[13px] text-slate-700">
            {banner}
          </div>
        )}

        {!editing && (
          <div className="grid grid-cols-4 gap-2">
            {specs.map((s) => (
              <button
                key={s.provider}
                type="button"
                onClick={() => setProvider(s.provider)}
                className={cx(
                  "rounded border px-3 py-2 text-[13px] transition",
                  provider === s.provider
                    ? "border-brand-500 bg-brand-50 font-medium text-brand-700"
                    : "border-slate-300 text-slate-700 hover:bg-slate-50",
                )}
              >
                {s.label.split(" ")[0]}
              </button>
            ))}
          </div>
        )}

        <TextField
          label="Display name"
          value={displayName}
          onChange={setDisplayName}
          error={errors.display_name}
          placeholder="Production Tenancy"
          required
        />

        <TextField
          label={spec.account_id_label}
          value={accountID}
          onChange={setAccountID}
          error={errors.native_account_id}
          placeholder={spec.account_id_placeholder}
          mono
          required
        />

        <div>
          <span className="block text-[12px] font-medium text-slate-700">
            Regions <span className="text-st-down">*</span>
          </span>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {spec.regions.map((rg) => {
              const on = regions.includes(rg);
              return (
                <button
                  key={rg}
                  type="button"
                  onClick={() =>
                    setRegions((cur) => (on ? cur.filter((x) => x !== rg) : [...cur, rg]))
                  }
                  aria-pressed={on}
                  className={cx(
                    "rounded border px-2 py-1 font-mono text-[11px] transition",
                    on
                      ? "border-brand-500 bg-brand-50 text-brand-700"
                      : "border-slate-300 text-slate-600 hover:bg-slate-50",
                  )}
                >
                  {rg}
                </button>
              );
            })}
          </div>
          {errors.regions && <p className="mt-1 text-[11px] text-st-down">{errors.regions}</p>}
        </div>

        {spec.fields.map((f) => (
          <TextField
            key={f.key}
            label={f.label}
            value={config[f.key] ?? ""}
            onChange={(v) => setConfig((c) => ({ ...c, [f.key]: v }))}
            error={errors[f.key]}
            placeholder={f.placeholder}
            help={f.help}
            required={f.required}
            mono
          />
        ))}

        <div className="rounded border border-slate-200 bg-slate-50 p-3">
          <TextField
            label={spec.credential.label}
            value={credRef}
            onChange={setCredRef}
            error={errors.credentials_ref}
            placeholder={spec.credential.placeholder}
            required={spec.credential.required}
            mono
          />
          <p className="mt-2 text-[11px] text-slate-600">{spec.credential.help}</p>
          {editing && (
            <p className="mt-1 text-[11px] text-slate-500">
              Leave unchanged to keep the current path.
            </p>
          )}
        </div>

        <InfoBanner>
          <strong>Least privilege.</strong> Discovery and metric collection need read access only.
          <ul className="mt-1.5 ml-4 list-disc space-y-0.5">
            {spec.permissions.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </InfoBanner>

        <div className="flex justify-end gap-2 border-t border-slate-200 pt-3">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={submit} disabled={saving}>
            {saving ? "Saving…" : editing ? "Save changes" : "Integrate Account"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function TextField({
  label,
  value,
  onChange,
  error,
  placeholder,
  help,
  required,
  mono,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  placeholder?: string;
  help?: string;
  required?: boolean;
  mono?: boolean;
}) {
  return (
    <label className="block">
      <span className="block text-[12px] font-medium text-slate-700">
        {label} {required && <span className="text-st-down">*</span>}
      </span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-invalid={error ? true : undefined}
        className={cx(
          "mt-1 w-full rounded border px-2.5 py-1.5 text-[13px] placeholder:text-slate-400",
          mono && "font-mono text-[12px]",
          error ? "border-st-down bg-st-down-bg/40" : "border-slate-300",
        )}
      />
      {error ? (
        <p className="mt-1 text-[11px] text-st-down">{error}</p>
      ) : help ? (
        <p className="mt-1 text-[11px] text-slate-500">{help}</p>
      ) : null}
    </label>
  );
}

/* ---------------------------------------------------------------- modal */

function Modal({
  title,
  children,
  onClose,
  wide,
}: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  // Escape closes the dialog; a modal that traps the user is worse than no modal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-slate-900/40 p-6">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cx(
          "w-full rounded-lg bg-white shadow-xl",
          wide ? "max-w-2xl" : "max-w-md",
        )}
      >
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h2 className="text-[15px] font-medium text-slate-800">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="grid size-7 place-items-center rounded text-slate-400 hover:bg-slate-100"
          >
            <X className="size-4" aria-hidden="true" />
          </button>
        </div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

export { Modal };
export function accountLastSeen(a: CloudAccount): string {
  return a.last_discovery_at ? absolute(a.last_discovery_at) : "never";
}

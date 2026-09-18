/**
 * Change password.
 *
 * Opened from the profile menu. Changing a password revokes every session,
 * including this one, so the form says so before submitting and hands off to the
 * login screen afterwards rather than leaving a shell whose requests all fail.
 */

import { useState } from "react";
import { AlertTriangle, Check, KeyRound, Loader2 } from "lucide-react";
import { ApiError, api } from "../lib/api";
import { cx } from "../lib/format";
import { Button, InfoBanner } from "./ui";
import { Modal } from "../pages/AdminCloudAccounts";

export function ChangePasswordModal({
  onClose,
  onSignedOut,
}: {
  onClose: () => void;
  onSignedOut: () => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [banner, setBanner] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  // Checked here as well as on the server. The server is authoritative; this is
  // only so the user is not told to wait for a round trip to learn the two
  // confirmations differ.
  function localProblems(): Record<string, string> {
    const f: Record<string, string> = {};
    if (!current) f.current_password = "Required.";
    if (next.length < 12) f.new_password = "Must be at least 12 characters.";
    if (next && next === current) f.new_password = "Must differ from the current password.";
    if (confirm !== next) f.confirm = "Does not match.";
    return f;
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    const problems = localProblems();
    if (Object.keys(problems).length > 0) {
      setErrors(problems);
      return;
    }
    setBusy(true);
    setErrors({});
    setBanner(null);
    try {
      await api.changePassword(current, next);
      setDone(true);
      // Every session was revoked server-side, so staying here would just produce
      // 401s. Give the confirmation a beat, then go to the login screen.
      window.setTimeout(onSignedOut, 1800);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === "invalid_credentials") {
          setErrors({ current_password: "Current password is incorrect." });
        } else if (err.fields) {
          setErrors(err.fields);
        } else {
          setBanner(err.message);
        }
      } else {
        setBanner("Could not reach the server.");
      }
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <Modal title="Password changed" onClose={onSignedOut}>
        <div className="flex items-start gap-3">
          <span className="grid size-9 shrink-0 place-items-center rounded-full bg-st-up-bg text-st-up">
            <Check className="size-5" aria-hidden="true" />
          </span>
          <div>
            <p className="text-[13px] text-slate-700">
              Your password has been changed and every session was signed out.
            </p>
            <p className="mt-1.5 text-[12px] text-slate-500">
              Taking you to the sign-in page…
            </p>
          </div>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="Change password" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {banner && (
          <div className="flex items-start gap-2 rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[12px] text-slate-700">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-st-down" aria-hidden="true" />
            <span>{banner}</span>
          </div>
        )}

        <Field
          label="Current password"
          value={current}
          onChange={setCurrent}
          error={errors.current_password}
          autoComplete="current-password"
          autoFocus
        />
        <Field
          label="New password"
          value={next}
          onChange={setNext}
          error={errors.new_password}
          autoComplete="new-password"
          help="At least 12 characters. Length matters more than symbols."
        />
        <Field
          label="Confirm new password"
          value={confirm}
          onChange={setConfirm}
          error={errors.confirm}
          autoComplete="new-password"
        />

        <InfoBanner>
          This signs you out everywhere, including here. A password change usually
          means the old one is no longer trusted, so leaving other sessions alive
          would defeat it.
        </InfoBanner>

        <div className="flex justify-end gap-2 border-t border-slate-200 pt-3">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" disabled={busy}>
            {busy ? (
              <>
                <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
                Changing…
              </>
            ) : (
              <>
                <KeyRound className="size-3.5" aria-hidden="true" />
                Change password
              </>
            )}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function Field({
  label,
  value,
  onChange,
  error,
  help,
  autoComplete,
  autoFocus,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  help?: string;
  autoComplete?: string;
  autoFocus?: boolean;
}) {
  return (
    <label className="block">
      <span className="block text-[12px] font-medium text-slate-700">{label}</span>
      <input
        type="password"
        value={value}
        autoComplete={autoComplete}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={error ? true : undefined}
        className={cx(
          "mt-1 w-full rounded border px-2.5 py-1.5 text-[13px]",
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

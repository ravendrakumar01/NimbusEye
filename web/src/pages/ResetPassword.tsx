/**
 * Set a new password from a reset link.
 *
 * Reached at /reset?token=… from the email. Rendered outside the authenticated
 * shell, because the whole point is that the person cannot sign in.
 *
 * The token is never echoed back into the page or the title, so it does not end up
 * in a screenshot, a browser history entry that gets shared, or a referrer header.
 */

import { useEffect, useState } from "react";
import { Activity, AlertTriangle, Check, Loader2, Lock } from "lucide-react";
import { ApiError, api } from "../lib/api";
import { cx } from "../lib/format";

export function ResetPassword({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  // Read the token, then strip it from the address bar. It stays in memory for the
  // one request that needs it; leaving it in the URL means it lands in history and
  // in any link the page might later navigate to.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const t = params.get("token");
    setToken(t);
    if (t) {
      window.history.replaceState({}, "", "/reset");
    }
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy || !token) return;
    setFieldError(null);
    setError(null);

    if (password.length < 12) {
      setFieldError("Must be at least 12 characters.");
      return;
    }
    if (password !== confirm) {
      setFieldError("The two passwords do not match.");
      return;
    }

    setBusy(true);
    try {
      await api.resetPassword(token, password);
      setDone(true);
      window.setTimeout(onDone, 2200);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.fields?.new_password) setFieldError(err.fields.new_password);
        else setError(err.message);
      } else {
        setError("Could not reach the server.");
      }
      setPassword("");
      setConfirm("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-slate-100 px-4 py-10">
      <div className="w-full max-w-[380px]">
        <div className="mb-6 flex flex-col items-center">
          <span className="grid size-11 place-items-center rounded-lg bg-go-500 text-white">
            <Activity className="size-6" aria-hidden="true" />
          </span>
          <h1 className="mt-3 text-[22px] font-bold tracking-tight">
            <span className="text-go-500">Nimbus</span>
            <span className="text-slate-800">Eye</span>
          </h1>
        </div>

        <div className="rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
          {done ? (
            <div className="flex items-start gap-3">
              <span className="grid size-9 shrink-0 place-items-center rounded-full bg-st-up-bg text-st-up">
                <Check className="size-5" aria-hidden="true" />
              </span>
              <div>
                <h2 className="text-[15px] font-medium text-slate-800">Password set</h2>
                <p className="mt-1 text-[13px] text-slate-600">
                  Every session was signed out. Taking you to the sign-in page…
                </p>
              </div>
            </div>
          ) : !token ? (
            <div>
              <h2 className="text-[15px] font-medium text-slate-800">Link incomplete</h2>
              <p className="mt-2 text-[13px] text-slate-600">
                This page needs a reset link from your email. Open the link from the message, or
                request a new one from the sign-in page.
              </p>
              <button
                type="button"
                onClick={onDone}
                className="mt-4 w-full rounded bg-go-500 px-3 py-2 text-[13px] font-medium text-white hover:bg-go-600"
              >
                Back to sign in
              </button>
            </div>
          ) : (
            <form onSubmit={submit}>
              <h2 className="text-[15px] font-medium text-slate-800">Choose a new password</h2>

              {error && (
                <div
                  role="alert"
                  className="mt-3 flex items-start gap-2 rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[12px] text-slate-700"
                >
                  <AlertTriangle
                    className="mt-0.5 size-3.5 shrink-0 text-st-down"
                    aria-hidden="true"
                  />
                  <span>{error}</span>
                </div>
              )}

              <Field
                label="New password"
                value={password}
                onChange={setPassword}
                error={fieldError ?? undefined}
                help="At least 12 characters. Length matters more than symbols."
                autoFocus
              />
              <Field label="Confirm new password" value={confirm} onChange={setConfirm} />

              <button
                type="submit"
                disabled={busy}
                className={cx(
                  "mt-5 flex w-full items-center justify-center gap-2 rounded px-3 py-2 text-[13px] font-medium text-white transition",
                  busy ? "cursor-not-allowed bg-slate-400" : "bg-go-500 hover:bg-go-600",
                )}
              >
                {busy && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
                {busy ? "Setting…" : "Set password"}
              </button>

              <p className="mt-4 text-[11px] leading-relaxed text-slate-500">
                The link works once and expires an hour after it was requested. Setting a new
                password ends every signed-in session.
              </p>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  error,
  help,
  autoFocus,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  help?: string;
  autoFocus?: boolean;
}) {
  return (
    <label className="mt-4 block">
      <span className="block text-[12px] font-medium text-slate-700">{label}</span>
      <span className="relative mt-1 block">
        <Lock
          className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400"
          aria-hidden="true"
        />
        <input
          type="password"
          autoComplete="new-password"
          autoFocus={autoFocus}
          required
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={error ? true : undefined}
          className={cx(
            "w-full rounded border py-2 pr-3 pl-8 text-[13px]",
            error ? "border-st-down bg-st-down-bg/40" : "border-slate-300",
          )}
        />
      </span>
      {error ? (
        <p className="mt-1 text-[11px] text-st-down">{error}</p>
      ) : help ? (
        <p className="mt-1 text-[11px] text-slate-500">{help}</p>
      ) : null}
    </label>
  );
}

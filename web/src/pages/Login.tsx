/**
 * Login page.
 *
 * Two things it is careful about:
 *
 * 1. It never distinguishes an unknown address from a wrong password. The server
 *    already refuses to, and echoing a more specific message would undo that and
 *    turn the form into an account-enumeration tool.
 * 2. When the server reports a lockout it shows the remaining time, because the
 *    alternative is a user retrying and extending their own lock.
 */

import { useEffect, useState } from "react";
import { Activity, AlertTriangle, Loader2, Lock, Mail } from "lucide-react";
import { ApiError, api } from "../lib/api";
import type { AuthUser } from "../lib/api";
import { cx } from "../lib/format";

export function Login({ onSignedIn }: { onSignedIn: (user: AuthUser) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [lockedFor, setLockedFor] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [needsBootstrap, setNeedsBootstrap] = useState(false);
  const [mode, setMode] = useState<"signin" | "forgot">("signin");
  const [sent, setSent] = useState<string | null>(null);

  // If no user exists the form cannot succeed, so say so instead of letting
  // someone guess at credentials that were never created.
  useEffect(() => {
    api
      .bootstrapStatus()
      .then((s) => setNeedsBootstrap(s.needs_bootstrap))
      .catch(() => setNeedsBootstrap(false));
  }, []);

  // Count the lock down so the button re-enables on its own.
  useEffect(() => {
    if (lockedFor === null || lockedFor <= 0) return;
    const t = window.setTimeout(() => setLockedFor((v) => (v === null ? null : v - 1)), 1000);
    return () => window.clearTimeout(t);
  }, [lockedFor]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    setLockedFor(null);
    try {
      const res = await api.login(email.trim(), password);
      onSignedIn(res.user);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === "account_locked") {
          setLockedFor(err.retryAfter ?? 120);
          setError(err.message);
        } else {
          setError(err.message);
        }
      } else {
        setError("Could not reach the server. Check your connection and try again.");
      }
      setPassword("");
    } finally {
      setBusy(false);
    }
  }

  async function requestReset(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.forgotPassword(email.trim());
      setSent(res.message);
    } catch (err) {
      // A missing mail relay is the one case worth reporting: otherwise the
      // response is identical whether or not the address exists, by design.
      setError(err instanceof ApiError ? err.message : "Could not reach the server.");
    } finally {
      setBusy(false);
    }
  }

  const locked = lockedFor !== null && lockedFor > 0;

  if (mode === "forgot") {
    return (
      <Shell>
        <form onSubmit={requestReset} className="rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
          <h2 className="text-[15px] font-medium text-slate-800">Reset your password</h2>

          {sent ? (
            <>
              <div className="mt-3 rounded border border-st-up/30 bg-st-up-bg px-3 py-2 text-[12px] text-slate-700">
                {sent}
              </div>
              <button
                type="button"
                onClick={() => {
                  setMode("signin");
                  setSent(null);
                }}
                className="mt-4 w-full rounded bg-go-500 px-3 py-2 text-[13px] font-medium text-white hover:bg-go-600"
              >
                Back to sign in
              </button>
            </>
          ) : (
            <>
              <p className="mt-2 text-[12px] leading-relaxed text-slate-600">
                Enter your email and we will send a link to choose a new password. The link works
                once and expires after an hour.
              </p>

              {error && (
                <div
                  role="alert"
                  className="mt-3 flex items-start gap-2 rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[12px] text-slate-700"
                >
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-st-down" aria-hidden="true" />
                  <span>{error}</span>
                </div>
              )}

              <label className="mt-4 block">
                <span className="block text-[12px] font-medium text-slate-700">Email</span>
                <span className="relative mt-1 block">
                  <Mail
                    className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400"
                    aria-hidden="true"
                  />
                  <input
                    type="email"
                    autoComplete="username"
                    required
                    autoFocus
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    placeholder="you@example.com"
                    className="w-full rounded border border-slate-300 py-2 pr-3 pl-8 text-[13px] placeholder:text-slate-400"
                  />
                </span>
              </label>

              <button
                type="submit"
                disabled={busy}
                className={cx(
                  "mt-5 flex w-full items-center justify-center gap-2 rounded px-3 py-2 text-[13px] font-medium text-white transition",
                  busy ? "cursor-not-allowed bg-slate-400" : "bg-go-500 hover:bg-go-600",
                )}
              >
                {busy && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
                {busy ? "Sending…" : "Send reset link"}
              </button>

              <button
                type="button"
                onClick={() => setMode("signin")}
                className="mt-3 w-full text-center text-[12px] text-brand-500 hover:underline"
              >
                Back to sign in
              </button>
            </>
          )}
        </form>
      </Shell>
    );
  }

  return (
    <Shell>
        <form
          onSubmit={submit}
          className="rounded-lg border border-slate-200 bg-white p-6 shadow-sm"
        >
          <h2 className="text-[15px] font-medium text-slate-800">Sign in</h2>

          {needsBootstrap && (
            <div className="mt-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-[12px] text-slate-700">
              No accounts exist on this server yet. Create the first one on the host:
              <code className="mt-1.5 block rounded bg-white px-2 py-1 font-mono text-[11px] text-slate-800">
                nimbuseye-api --create-user you@example.com --name "Your Name"
              </code>
            </div>
          )}

          {error && (
            <div
              role="alert"
              className="mt-3 flex items-start gap-2 rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[12px] text-slate-700"
            >
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-st-down" aria-hidden="true" />
              <span>
                {error}
                {locked && (
                  <span className="num mt-0.5 block text-slate-600">
                    Try again in {lockedFor}s.
                  </span>
                )}
              </span>
            </div>
          )}

          <label className="mt-4 block">
            <span className="block text-[12px] font-medium text-slate-700">Email</span>
            <span className="relative mt-1 block">
              <Mail
                className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400"
                aria-hidden="true"
              />
              <input
                type="email"
                autoComplete="username"
                required
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="you@example.com"
                className="w-full rounded border border-slate-300 py-2 pr-3 pl-8 text-[13px] placeholder:text-slate-400"
              />
            </span>
          </label>

          <label className="mt-3 block">
            <span className="block text-[12px] font-medium text-slate-700">Password</span>
            <span className="relative mt-1 block">
              <Lock
                className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400"
                aria-hidden="true"
              />
              <input
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••••••"
                className="w-full rounded border border-slate-300 py-2 pr-3 pl-8 text-[13px] placeholder:text-slate-400"
              />
            </span>
          </label>

          <button
            type="submit"
            disabled={busy || locked}
            className={cx(
              "mt-5 flex w-full items-center justify-center gap-2 rounded px-3 py-2 text-[13px] font-medium text-white transition",
              busy || locked ? "cursor-not-allowed bg-slate-400" : "bg-go-500 hover:bg-go-600",
            )}
          >
            {busy && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
            {busy ? "Signing in…" : locked ? `Locked (${lockedFor}s)` : "Sign in"}
          </button>

          <button
            type="button"
            onClick={() => {
              setMode("forgot");
              setError(null);
              setPassword("");
            }}
            className="mt-3 w-full text-center text-[12px] text-brand-500 hover:underline"
          >
            Forgot your password?
          </button>

          <p className="mt-4 text-[11px] leading-relaxed text-slate-500">
            Sessions last 12 hours and do not extend on activity. Five failed attempts lock the
            account for a few minutes, doubling with each further failure.
          </p>
        </form>

      <p className="mt-4 text-center text-[11px] text-slate-400">
        Multi-factor authentication is not built yet.
      </p>
    </Shell>
  );
}

/** Shared page chrome for the sign-in and reset-request views. */
function Shell({ children }: { children: React.ReactNode }) {
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
          <p className="mt-0.5 text-[12px] text-slate-500">multi-cloud monitoring</p>
        </div>
        {children}
      </div>
    </div>
  );
}

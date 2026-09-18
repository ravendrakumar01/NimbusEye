/**
 * Profile menu — the panel behind the avatar.
 *
 * Modelled on the reference console's account panel: identity, role, timezone,
 * then the display preferences and sign-out. Everything here either works or says
 * plainly that it does not; a settings panel full of inert toggles is worse than a
 * short one.
 *
 * The compact-rail preference and the timezone persist locally. Sign out ends the
 * server-side session, so it takes effect everywhere rather than just clearing a
 * local flag. Night Mode is shown but disabled: a dark theme needs every surface
 * colour reworked, not a class toggle, and it says so.
 */

import { useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import { useState } from "react";
import { Clock, Keyboard, KeyRound, LogOut, Moon, PanelLeft, ShieldCheck, UserRound, X } from "lucide-react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { cx } from "../lib/format";
import { ChangePasswordModal } from "./ChangePassword";

export interface Preferences {
  nightMode: boolean;
  compactRail: boolean;
  timezone: string;
}

export const PREFS_KEY = "nimbuseye.prefs";

/** Reads stored preferences, falling back to the browser's own timezone. */
export function loadPreferences(): Preferences {
  const fallback: Preferences = {
    nightMode: false,
    compactRail: false,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Kolkata",
  };
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) return fallback;
    return { ...fallback, ...(JSON.parse(raw) as Partial<Preferences>) };
  } catch {
    // A corrupt value must not break the shell.
    return fallback;
  }
}

export function savePreferences(p: Preferences) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(p));
  } catch {
    /* private browsing or a full quota; preferences simply do not persist */
  }
}

export function ProfileMenu({
  open,
  onClose,
  prefs,
  onChange,
  mode,
  version,
  monitorCount,
}: {
  open: boolean;
  onClose: () => void;
  prefs: Preferences;
  onChange: (p: Preferences) => void;
  mode?: string;
  version?: string;
  monitorCount?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const { user, signOut } = useAuth();
  const [signingOut, setSigningOut] = useState(false);
  const [changing, setChanging] = useState(false);

  async function doSignOut() {
    setSigningOut(true);
    try {
      await api.logout();
    } catch {
      // The local session is dropped regardless: if the server call failed the
      // cookie may still be live, but leaving the user apparently signed in when
      // they asked to leave is the worse outcome.
    } finally {
      signOut();
    }
  }

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    document.addEventListener("keydown", onKey);
    // Deferred: attaching immediately would catch the click that opened the menu.
    const t = window.setTimeout(() => document.addEventListener("mousedown", onClick), 0);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onClick);
      window.clearTimeout(t);
    };
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      ref={ref}
      role="dialog"
      aria-label="Account"
      className="absolute top-[52px] right-2 z-50 w-[340px] overflow-hidden rounded-lg border border-slate-200 bg-white shadow-xl"
    >
      <div className="flex items-start gap-3 px-4 py-3.5">
        <span className="grid size-10 shrink-0 place-items-center rounded-full bg-slate-800 text-white">
          <UserRound className="size-5" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium text-slate-800">
            {user.display_name}
          </div>
          <div className="truncate text-[11px] text-slate-500">{user.email}</div>
          <div className="mt-1.5 inline-flex items-center gap-1 text-[11px] text-slate-600">
            <ShieldCheck className="size-3 text-st-up" aria-hidden="true" />
            Role: {user.role.charAt(0).toUpperCase() + user.role.slice(1)}
            <span className="text-slate-400">· {user.tenant_slug}</span>
          </div>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="grid size-6 place-items-center rounded text-slate-400 hover:bg-slate-100"
        >
          <X className="size-3.5" aria-hidden="true" />
        </button>
      </div>

      <div className="border-t border-slate-200 px-4 py-2.5">
        <div className="flex items-center gap-1.5 text-[11px] text-slate-500">
          <Clock className="size-3" aria-hidden="true" />
          Time zone
        </div>
        <div className="num mt-0.5 text-[13px] text-slate-800">
          {user.timezone || prefs.timezone}
        </div>
        <div className="num mt-0.5 text-[11px] text-slate-500">
          {new Date().toLocaleString("en-IN", {
            timeZone: user.timezone || prefs.timezone,
            hour12: true,
          })}
        </div>
      </div>

      <div className="border-t border-slate-200">
        <Toggle
          icon={Moon}
          label="Night Mode"
          hint="needs every surface colour reworked"
          checked={false}
          disabled
          onChange={() => undefined}
        />
        <Toggle
          icon={PanelLeft}
          label="Compact side menu"
          hint="hide labels on the icon rail"
          checked={prefs.compactRail}
          onChange={(v) => onChange({ ...prefs, compactRail: v })}
        />
      </div>

      <div className="border-t border-slate-200 py-1">
        <button
          type="button"
          onClick={() => setChanging(true)}
          className="flex w-full items-center gap-2.5 px-4 py-2 text-left text-[13px] text-slate-700 hover:bg-slate-50"
        >
          <KeyRound className="size-4 text-slate-400" aria-hidden="true" />
          Change password
        </button>
        <Row icon={Keyboard} label="Keyboard shortcuts" hint="Ctrl-K or / to search" />
        <Link
          to="/admin/cloud-accounts"
          onClick={onClose}
          className="flex items-center gap-2.5 px-4 py-2 text-[13px] text-slate-700 hover:bg-slate-50"
        >
          <ShieldCheck className="size-4 text-slate-400" aria-hidden="true" />
          Cloud accounts
        </Link>
      </div>

      <div className="border-t border-slate-200 px-4 py-2.5">
        <div className="flex items-center justify-between text-[11px] text-slate-500">
          <span>NimbusEye {version ?? ""}</span>
          <span className="num">{monitorCount !== undefined ? `${monitorCount} monitors` : ""}</span>
        </div>
        {mode === "mock" && (
          <div className="mt-1 text-[11px] text-st-discovery">
            Serving generated data — not real infrastructure
          </div>
        )}
      </div>

      <div className="border-t border-slate-200 p-2">
        <button
          type="button"
          onClick={doSignOut}
          disabled={signingOut}
          className="flex w-full items-center gap-2.5 rounded px-2 py-2 text-[13px] text-slate-700 hover:bg-slate-50 disabled:text-slate-400"
        >
          <LogOut className="size-4" aria-hidden="true" />
          {signingOut ? "Signing out…" : "Sign out"}
        </button>
      </div>

      {changing && (
        <ChangePasswordModal
          onClose={() => setChanging(false)}
          onSignedOut={() => {
            setChanging(false);
            signOut();
          }}
        />
      )}
    </div>
  );
}

function Toggle({
  icon: Icon,
  label,
  hint,
  checked,
  onChange,
  disabled,
}: {
  icon: typeof Moon;
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      title={disabled ? "Not built yet" : undefined}
      onClick={() => onChange(!checked)}
      className={cx(
        "flex w-full items-center gap-2.5 px-4 py-2.5 text-left",
        disabled ? "cursor-not-allowed" : "hover:bg-slate-50",
      )}
    >
      <Icon className={cx("size-4 shrink-0", disabled ? "text-slate-300" : "text-slate-400")} aria-hidden="true" />
      <span className="min-w-0 flex-1">
        <span className={cx("block text-[13px]", disabled ? "text-slate-400" : "text-slate-700")}>
          {label}
          {disabled && <span className="ml-1.5 text-[10px]">not built yet</span>}
        </span>
        {hint && <span className="block text-[11px] text-slate-500">{hint}</span>}
      </span>
      <span
        className={cx(
          "relative h-4.5 w-8 shrink-0 rounded-full transition",
          checked ? "bg-brand-500" : disabled ? "bg-slate-200" : "bg-slate-300",
        )}
      >
        <span
          className={cx(
            "absolute top-0.5 size-3.5 rounded-full bg-white transition-all",
            checked ? "left-4" : "left-0.5",
          )}
        />
      </span>
    </button>
  );
}

function Row({
  icon: Icon,
  label,
  hint,
}: {
  icon: typeof Keyboard;
  label: string;
  hint?: string;
}) {
  return (
    <div className="flex items-center gap-2.5 px-4 py-2 text-[13px] text-slate-700">
      <Icon className="size-4 shrink-0 text-slate-400" aria-hidden="true" />
      <span className="flex-1">{label}</span>
      {hint && <span className="text-[11px] text-slate-500">{hint}</span>}
    </div>
  );
}

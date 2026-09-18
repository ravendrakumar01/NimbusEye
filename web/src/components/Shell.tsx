/**
 * Application shell, rebuilt against the reference screenshots.
 *
 * Three columns:
 *   1. a ~50px icon rail that selects a domain (Home, Alarms, Web, ... Admin),
 *      with a live clock pinned to its bottom;
 *   2. a ~210px context panel whose contents depend on the selected rail item;
 *   3. the white content canvas.
 *
 * The layout, navigation model and interaction pattern follow the console this
 * replaces, because that structure maps to how an operator actually works. All
 * styling is written here from scratch — no stylesheets, icons or images are
 * taken from the original.
 */

import { useEffect, useMemo, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useSearchParams } from "react-router-dom";
import { Bell, ChevronDown, ChevronLeft, ChevronRight, Plus, Search, UserRound } from "lucide-react";
import { api } from "../lib/api";
import type { Severity } from "../lib/api";
import { useAsync, usePolling } from "../lib/hooks";
import { RAIL, sectionForPath } from "../lib/nav";
import type { PanelItem, RailSection } from "../lib/nav";
import { PROVIDER_LABEL, STATUS_STYLE, cx, num } from "../lib/format";
import { CommandPalette, usePaletteShortcut } from "./CommandPalette";
import { ProfileMenu, loadPreferences, savePreferences } from "./ProfileMenu";
import type { Preferences } from "./ProfileMenu";

export function Shell() {
  const location = useLocation();
  const section = sectionForPath(location.pathname);

  const [panelOpen, setPanelOpen] = useState(true);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [prefs, setPrefs] = useState<Preferences>(loadPreferences);
  usePaletteShortcut(() => setPaletteOpen(true));

  function updatePrefs(next: Preferences) {
    setPrefs(next);
    savePreferences(next);
  }

  const summary = useAsync(() => api.summary(), []);
  usePolling(summary.reload, 30_000);

  const openAlarms = summary.data
    ? (summary.data.open_alarms.down ?? 0) +
      (summary.data.open_alarms.critical ?? 0) +
      (summary.data.open_alarms.trouble ?? 0)
    : 0;

  return (
    <div className="flex h-full flex-col">
      <TopBar
        totalResources={summary.data?.total}
        alarmCount={openAlarms}
        panelOpen={panelOpen}
        onTogglePanel={() => setPanelOpen((v) => !v)}
        onOpenPalette={() => setPaletteOpen(true)}
        prefs={prefs}
        onPrefsChange={updatePrefs}
      />
      <div className="flex min-h-0 flex-1">
        <IconRail active={section.key} alarmCount={openAlarms} compact={prefs.compactRail} />
        {panelOpen && <ContextPanel section={section} />}
        <main className="min-w-0 flex-1 overflow-y-auto bg-white scroll-thin">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  );
}

/* ---------------------------------------------------------------- top bar */

function TopBar({
  totalResources,
  alarmCount,
  panelOpen,
  onTogglePanel,
  onOpenPalette,
  prefs,
  onPrefsChange,
}: {
  totalResources?: number;
  alarmCount: number;
  panelOpen: boolean;
  onTogglePanel: () => void;
  onOpenPalette: () => void;
  prefs: Preferences;
  onPrefsChange: (p: Preferences) => void;
}) {
  const health = useAsync(() => api.health(), []);
  const [profileOpen, setProfileOpen] = useState(false);

  return (
    <header className="relative flex h-[52px] shrink-0 items-center gap-3 border-b border-slate-200 bg-white px-4">
      <Link to="/" className="flex shrink-0 flex-col leading-none" aria-label="NimbusEye home">
        <span className="text-[19px] font-bold tracking-tight">
          <span className="text-go-500">Nimbus</span>
          <span className="text-slate-800">Eye</span>
        </span>
        <span className="mt-0.5 text-[9px] tracking-wide text-slate-400">
          multi-cloud monitoring
        </span>
      </Link>

      {/* Collapse the context panel. Useful on a laptop: the panel plus the rail
          is 264px of a 1366px screen. */}
      <button
        type="button"
        onClick={onTogglePanel}
        aria-label={panelOpen ? "Hide side menu" : "Show side menu"}
        aria-expanded={panelOpen}
        title={panelOpen ? "Hide side menu" : "Show side menu"}
        className="grid size-6 shrink-0 place-items-center rounded border border-slate-300 text-slate-500 hover:bg-slate-100"
      >
        <ChevronLeft
          className={cx("size-3.5 transition", !panelOpen && "rotate-180")}
          aria-hidden="true"
        />
      </button>

      {/* A button, not an input: it opens the command palette, which is where the
          typing actually happens. Rendering a real input here would give two
          places to type and one of them would not work. */}
      <button
        type="button"
        onClick={onOpenPalette}
        className="hidden max-w-[420px] flex-1 items-center gap-2 rounded-md border border-slate-300 px-2.5 py-1.5 text-left text-sm text-slate-400 hover:border-slate-400 hover:bg-slate-50 sm:flex"
      >
        <Search className="size-4 shrink-0" aria-hidden="true" />
        <span className="flex-1">Search monitors, or type / for commands</span>
        <kbd className="shrink-0 rounded border border-slate-300 px-1.5 py-0.5 text-[10px] text-slate-500">
          Ctrl K
        </kbd>
      </button>

      <div className="ml-auto flex items-center gap-2">
        {health.data?.mode === "mock" && (
          <span className="rounded-full bg-st-discovery-bg px-2 py-0.5 text-[11px] font-medium text-st-discovery ring-1 ring-st-discovery/30">
            Demo data
          </span>
        )}
        {totalResources !== undefined && (
          <span className="num hidden text-xs text-slate-500 xl:inline">
            {num(totalResources)} monitors
          </span>
        )}

        {/* Alarms. The count is the one number worth putting in the chrome, and
            it is a link rather than a dead bell. */}
        <NavLink
          to="/alarms"
          title={alarmCount > 0 ? `${alarmCount} open alarms` : "No open alarms"}
          className="relative grid size-8 place-items-center rounded text-slate-500 hover:bg-slate-100"
        >
          <Bell className="size-4" aria-hidden="true" />
          {alarmCount > 0 && (
            <span className="num absolute top-0.5 right-0 rounded-full bg-st-down px-1 text-[9px] font-bold text-white">
              {alarmCount > 99 ? "99+" : alarmCount}
            </span>
          )}
          <span className="sr-only">Alarms</span>
        </NavLink>

        <button
          type="button"
          onClick={onOpenPalette}
          title="Search and commands (Ctrl-K)"
          className="grid size-8 place-items-center rounded text-slate-500 hover:bg-slate-100 sm:hidden"
        >
          <Search className="size-4" aria-hidden="true" />
          <span className="sr-only">Search</span>
        </button>

        <button
          type="button"
          onClick={() => setProfileOpen((v) => !v)}
          aria-expanded={profileOpen}
          aria-label="Account menu"
          className={cx(
            "grid size-8 place-items-center rounded-full text-white transition",
            profileOpen ? "bg-brand-600 ring-2 ring-brand-500/40" : "bg-slate-800 hover:bg-slate-700",
          )}
        >
          <UserRound className="size-4" aria-hidden="true" />
        </button>
      </div>

      <ProfileMenu
        open={profileOpen}
        onClose={() => setProfileOpen(false)}
        prefs={prefs}
        onChange={onPrefsChange}
        mode={health.data?.mode}
        version={health.data?.version}
        monitorCount={totalResources}
      />
    </header>
  );
}

/* -------------------------------------------------------------- icon rail */

function IconRail({
  active,
  alarmCount,
  compact,
}: {
  active: string;
  alarmCount: number;
  compact: boolean;
}) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    // The original keeps a live clock at the foot of the rail. It is genuinely
    // useful: screenshots and shared views become self-dating.
    const t = window.setInterval(() => setNow(new Date()), 30_000);
    return () => window.clearInterval(t);
  }, []);

  return (
    <nav
      className={cx(
        "flex shrink-0 flex-col items-stretch bg-rail",
        compact ? "w-[44px]" : "w-[52px]",
      )}
      aria-label="Sections"
    >
      <div className="flex-1 overflow-y-auto scroll-thin">
        {RAIL.map((s) => {
          const isActive = s.key === active;
          const Icon = s.icon;
          return (
            <NavLink
              key={s.key}
              to={s.to}
              title={s.label}
              className={cx(
                "relative flex flex-col items-center gap-0.5 py-2.5 transition",
                isActive
                  ? "bg-rail-active text-rail-accent"
                  : "text-rail-text hover:bg-rail-hover",
              )}
            >
              {isActive && (
                <span
                  aria-hidden="true"
                  className="absolute inset-y-0 right-0 w-0.5 bg-rail-accent"
                />
              )}
              <span className="relative">
                <Icon className="size-[19px]" aria-hidden="true" />
                {s.key === "alarms" && alarmCount > 0 && (
                  <span className="num absolute -top-1.5 -right-2 rounded-full bg-st-down px-1 text-[9px] font-bold text-white">
                    {alarmCount > 99 ? "99+" : alarmCount}
                  </span>
                )}
              </span>
              {!compact && <span className="text-[9.5px] leading-tight">{s.label}</span>}
            </NavLink>
          );
        })}


      </div>

      <div className="border-t border-white/5 px-1 py-2 text-center text-rail-muted">
        <div className="num text-[9.5px] leading-tight">
          {now.toLocaleTimeString("en-IN", {
            hour: "2-digit",
            minute: "2-digit",
            hour12: true,
          })}
        </div>
        <div className="num text-[9.5px] leading-tight">
          {now.toLocaleDateString("en-IN", { day: "numeric", month: "short" })}
        </div>
      </div>
    </nav>
  );
}

/* ---------------------------------------------------------- context panel */

function ContextPanel({ section }: { section: RailSection }) {
  return (
    <aside
      className="flex w-[212px] shrink-0 flex-col overflow-y-auto border-r border-panel-border bg-panel scroll-thin"
      aria-label={`${section.label} navigation`}
    >
      {section.key === "alarms" ? (
        <AlarmsPanel />
      ) : section.key === "cloud" ? (
        <CloudPanel />
      ) : (
        <GenericPanel section={section} />
      )}
    </aside>
  );
}

function PanelHeading({ children }: { children: React.ReactNode }) {
  return (
    <div className="border-b border-panel-border px-3 py-2.5 text-[13px] font-medium text-panel-text">
      {children}
    </div>
  );
}

function GenericPanel({ section }: { section: RailSection }) {
  const location = useLocation();
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  // Rows following a collapsed group header are hidden with it.
  const rows: (PanelItem & { hidden?: boolean })[] = [];
  let currentGroup: string | null = null;
  for (const item of section.items) {
    if (item.group) {
      currentGroup = item.label;
      rows.push(item);
      continue;
    }
    if (item.indent && currentGroup) {
      const isOpen = collapsed[currentGroup] ?? section.items.find((i) => i.label === currentGroup)?.open ?? false;
      rows.push({ ...item, hidden: !isOpen });
      continue;
    }
    currentGroup = null;
    rows.push(item);
  }

  return (
    <>
      {section.panelTitle && <PanelHeading>{section.panelTitle}</PanelHeading>}
      <ul className="py-1">
        {rows.map((item, i) => {
          if (item.hidden) return null;

          if (item.group) {
            const open =
              collapsed[item.label] ?? section.items.find((x) => x.label === item.label)?.open ?? false;
            return (
              <li key={`${item.label}-${i}`}>
                <button
                  type="button"
                  onClick={() => setCollapsed((c) => ({ ...c, [item.label]: !open }))}
                  aria-expanded={open}
                  className="flex w-full items-center justify-between px-3 py-[7px] text-[13px] text-panel-text hover:bg-panel-hover"
                >
                  <span>{item.label}</span>
                  {open ? (
                    <ChevronDown className="size-3.5 text-panel-muted" aria-hidden="true" />
                  ) : (
                    <ChevronRight className="size-3.5 text-panel-muted" aria-hidden="true" />
                  )}
                </button>
              </li>
            );
          }

          const isActive =
            item.to !== undefined &&
            (item.to.includes("?")
              ? location.pathname + location.search === item.to
              : location.pathname === item.to);

          const inner = (
            <>
              <span className={cx("truncate", item.indent && "pl-3")}>{item.label}</span>
              <span className="ml-auto flex shrink-0 items-center gap-1.5">
                {item.badge && (
                  <span
                    className={cx(
                      "num rounded px-1 text-[10px] font-semibold",
                      item.badge === "New"
                        ? "bg-go-500 text-white"
                        : "bg-brand-500 text-white",
                    )}
                  >
                    {item.badge}
                  </span>
                )}
                {item.add && <Plus className="size-3.5 text-panel-muted" aria-hidden="true" />}
              </span>
            </>
          );

          return (
            <li key={`${item.label}-${i}`}>
              {item.to ? (
                <Link
                  to={item.to}
                  className={cx(
                    "flex items-center px-3 py-[7px] text-[13px]",
                    isActive
                      ? "bg-panel-active font-medium text-white"
                      : "text-panel-text hover:bg-panel-hover",
                  )}
                >
                  {inner}
                </Link>
              ) : (
                // Present in the original but not implemented here. Shown greyed
                // out rather than hidden, so the gap is visible instead of
                // pretending the feature does not exist.
                <span
                  title="Not implemented yet"
                  className="flex cursor-not-allowed items-center px-3 py-[7px] text-[13px] text-panel-muted/60"
                >
                  {inner}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </>
  );
}

/** Alarms panel: severity matrix of Open vs Acknowledged, plus event categories. */
function AlarmsPanel() {
  const [params, setParams] = useSearchParams();
  const active = params.get("severity") ?? "";
  const activeState = params.get("state") ?? "";

  const summary = useAsync(() => api.summary(), []);
  const acked = useAsync(() => api.alarms({ state: ["acknowledged"], page_size: 1 }), []);
  usePolling(() => {
    summary.reload();
    acked.reload();
  }, 30_000);

  const severities: Severity[] = ["down", "critical", "trouble", "info"];
  const open = summary.data?.open_alarms ?? {};
  const total = severities.reduce((a, s) => a + (open[s] ?? 0), 0);

  function select(sev: Severity, state: "open" | "acknowledged") {
    const next = new URLSearchParams();
    next.set("severity", sev);
    next.set("state", state);
    setParams(next, { replace: true });
  }

  return (
    <>
      <PanelHeading>Infrastructure Events</PanelHeading>

      <div
        className={cx(
          "border-b border-panel-border px-3 py-2 text-[13px]",
          !active ? "bg-panel-warm font-medium text-white" : "text-panel-text",
        )}
      >
        <button type="button" onClick={() => setParams(new URLSearchParams(), { replace: true })}>
          Active Alarms ({num(total)})
        </button>
      </div>

      <div className="px-3 py-3">
        <div className="mb-2 grid grid-cols-2 gap-2 text-center text-[11px] text-panel-muted">
          <span>Open</span>
          <span>Acknowledged</span>
        </div>
        <div className="space-y-1.5">
          {severities.map((sev) => {
            const style = STATUS_STYLE[sev === "info" ? "maintenance" : sev];
            return (
              <div key={sev} className="grid grid-cols-2 gap-2">
                {(["open", "acknowledged"] as const).map((state) => (
                  <button
                    key={state}
                    type="button"
                    onClick={() => select(sev, state)}
                    title={`${sev} · ${state}`}
                    className={cx(
                      "num flex items-center justify-end gap-1 rounded-sm border-l-[3px] px-2 py-1 text-[13px] transition",
                      active === sev && activeState === state
                        ? "bg-panel-active text-white"
                        : "bg-[#262626] text-panel-text hover:bg-panel-hover",
                    )}
                    style={{
                      borderLeftColor: `var(--color-st-${sev === "info" ? "maintenance" : sev})`,
                    }}
                  >
                    <span className={cx("text-[10px]", style.text)}>●</span>
                    {state === "open"
                      ? num(open[sev] ?? 0)
                      : num(state === "acknowledged" ? (acked.data?.total ?? 0) : 0)}
                  </button>
                ))}
              </div>
            );
          })}
        </div>
      </div>

      <ul className="border-t border-panel-border">
        {[
          { label: "Confirmed Anomalies", value: 0 },
          { label: "AppLog Errors", value: 0 },
          { label: "APM/RUM", value: 0 },
          { label: "On-Premise Pollers", value: 0 },
        ].map((row) => (
          <li
            key={row.label}
            className="flex items-center justify-between border-b border-panel-border border-l-[3px] border-l-go-500 px-3 py-2 text-[13px] text-panel-text"
          >
            <span className="truncate">{row.label}</span>
            <span className="num text-panel-muted">{row.value}</span>
          </li>
        ))}
      </ul>
    </>
  );
}

/** Cloud panel: provider tab strip, then the resource types for that provider. */
function CloudPanel() {
  const [params, setParams] = useSearchParams();
  const provider = params.get("provider") ?? "oci";
  const activeType = params.get("type") ?? "";
  const [search, setSearch] = useState("");

  const filters = useAsync(() => api.filters(), []);
  const accounts = useAsync(() => api.accounts(), []);

  const account = accounts.data?.items.find((a) => a.provider === provider);

  const types = useMemo(() => {
    const list = (filters.data?.resource_types ?? []).filter((t) => t.provider === provider);
    const q = search.trim().toLowerCase();
    return q ? list.filter((t) => t.display_name.toLowerCase().includes(q)) : list;
  }, [filters.data, provider, search]);

  function setProvider(p: string) {
    const next = new URLSearchParams();
    next.set("provider", p);
    setParams(next, { replace: true });
  }

  function setType(code: string | null) {
    const next = new URLSearchParams();
    next.set("provider", provider);
    if (code) next.set("type", code);
    setParams(next, { replace: true });
  }

  const tabs = [
    { key: "aws", label: "AWS" },
    { key: "azure", label: "Azure" },
    { key: "gcp", label: "GCP" },
    { key: "oci", label: "OCI" },
  ];

  return (
    <>
      <div className="grid grid-cols-4 border-b border-panel-border">
        {tabs.map((t) => (
          <button
            key={t.key}
            type="button"
            onClick={() => setProvider(t.key)}
            className={cx(
              "flex flex-col items-center gap-0.5 py-2 text-[10px] transition",
              provider === t.key
                ? "bg-rail-active text-rail-accent"
                : "text-panel-text hover:bg-panel-hover",
            )}
          >
            <span className="text-[13px] leading-none font-semibold">
              {t.label === "Azure" ? "Az" : t.label === "GCP" ? "G" : t.label === "AWS" ? "aws" : "OCI"}
            </span>
            <span>{t.label}</span>
          </button>
        ))}
      </div>

      <ul className="border-b border-panel-border py-1">
        <li className="cursor-not-allowed px-3 py-[7px] text-[13px] text-panel-muted/60">
          Help Assistant
        </li>
        <li className="cursor-not-allowed px-3 py-[7px] text-[13px] text-panel-muted/60">
          Integrate {PROVIDER_LABEL[provider] ?? provider} Monitor
        </li>
        <li>
          <button
            type="button"
            onClick={() => setType(null)}
            className={cx(
              "w-full px-3 py-[7px] text-left text-[13px]",
              !activeType
                ? "bg-panel-active font-medium text-white"
                : "text-panel-text hover:bg-panel-hover",
            )}
          >
            Cloud Resources
          </button>
        </li>
      </ul>

      {account && (
        <div className="flex items-center justify-between border-b border-panel-border px-3 py-2 text-[12px] font-semibold tracking-wide text-panel-text uppercase">
          <span className="truncate" title={account.native_account_id}>
            {account.display_name.split(" (")[0]}
          </span>
          <ChevronDown className="size-3.5 shrink-0 text-panel-muted" aria-hidden="true" />
        </div>
      )}

      <div className="px-2 py-2">
        <label className="relative block">
          <span className="sr-only">Filter resource types</span>
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search"
            className="w-full rounded border border-panel-border bg-[#262626] py-1 pr-7 pl-2 text-[12px] text-panel-text placeholder:text-panel-muted"
          />
          <Search
            className="pointer-events-none absolute top-1/2 right-2 size-3.5 -translate-y-1/2 text-panel-muted"
            aria-hidden="true"
          />
        </label>
      </div>

      <ul className="pb-2">
        {types.length === 0 ? (
          <li className="px-3 py-2 text-[12px] text-panel-muted">No matching types</li>
        ) : (
          types.map((t) => (
            <li key={t.code}>
              <button
                type="button"
                onClick={() => setType(t.code)}
                className={cx(
                  "w-full truncate px-3 py-[7px] text-left text-[13px]",
                  activeType === t.code
                    ? "bg-panel-active font-medium text-white"
                    : "text-panel-text hover:bg-panel-hover",
                )}
              >
                {t.display_name}
              </button>
            </li>
          ))
        )}
      </ul>
    </>
  );
}

/**
 * Command palette — the top bar's search.
 *
 * The reference console's search box accepts slash-commands (`/add-monitors`,
 * `/admin-actions`) as well as free text, so it is a command palette rather than
 * a filter. That is the better design to copy: one control that both finds a
 * monitor and navigates the product, reachable from the keyboard.
 *
 * Opens on Ctrl/Cmd-K, on "/" anywhere outside a text field, or by clicking the
 * search box.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  ArrowRight,
  BellRing,
  Cloud,
  Cog,
  Container,
  CornerDownLeft,
  FileText,
  Globe,
  LayoutDashboard,
  Plus,
  Search,
  Sparkles,
  Wallet,
} from "lucide-react";
import { api } from "../lib/api";
import type { Resource } from "../lib/api";
import { useDebounced } from "../lib/hooks";
import { STATUS_LABEL, STATUS_STYLE, cx } from "../lib/format";

interface Command {
  id: string;
  label: string;
  hint?: string;
  icon: typeof Globe;
  to: string;
  keywords: string;
}

/** Navigation and actions, matched by label or keyword. */
const COMMANDS: Command[] = [
  { id: "add", label: "Add Monitor", hint: "create a website, port, DNS or certificate check", icon: Plus, to: "/add-monitor", keywords: "add create new monitor website check" },
  { id: "start", label: "Getting Started", hint: "what to monitor first", icon: Sparkles, to: "/getting-started", keywords: "getting started welcome onboarding setup begin" },
  { id: "home", label: "Monitor Status", hint: "all monitors", icon: LayoutDashboard, to: "/", keywords: "home dashboard monitors status" },
  { id: "alarms", label: "Alarms", hint: "open and acknowledged alerts", icon: BellRing, to: "/alarms", keywords: "alarms alerts incidents" },
  { id: "cloud", label: "Cloud Resources", hint: "OCI, AWS, Azure, GCP", icon: Cloud, to: "/cloud", keywords: "cloud oci aws azure gcp resources" },
  { id: "k8s", label: "Kubernetes", icon: Container, to: "/kubernetes", keywords: "kubernetes k8s clusters nodes pods" },
  { id: "web", label: "Web & Synthetic Checks", icon: Globe, to: "/web", keywords: "web website synthetic http ssl dns" },
  { id: "accounts", label: "Cloud Accounts", hint: "connect or edit a tenancy", icon: Cog, to: "/admin/cloud-accounts", keywords: "admin accounts tenancy credentials connect integrate" },
  { id: "outages", label: "Outages", icon: FileText, to: "/outages", keywords: "outages downtime history" },
  { id: "reports", label: "Reports", icon: FileText, to: "/reports", keywords: "reports availability sla" },
  { id: "finops", label: "Nimbus FinOps", hint: "cost", icon: Wallet, to: "/finops", keywords: "cost finops spend budget billing" },
];

export function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [monitors, setMonitors] = useState<Resource[]>([]);
  const [searching, setSearching] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounced = useDebounced(query, 200);

  useEffect(() => {
    if (open) {
      setQuery("");
      setCursor(0);
      setMonitors([]);
      // Focus after paint, or the browser drops it on a freshly mounted node.
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  // Monitor lookup. Slash-prefixed input is a command, so no search is issued.
  useEffect(() => {
    const q = debounced.trim();
    if (!open || q.length < 2 || q.startsWith("/")) {
      setMonitors([]);
      return;
    }
    let cancelled = false;
    setSearching(true);
    api
      .resources({ q, page_size: 6, sort: "status" })
      .then((r) => {
        if (!cancelled) setMonitors(r.items);
      })
      .catch(() => {
        if (!cancelled) setMonitors([]);
      })
      .finally(() => {
        if (!cancelled) setSearching(false);
      });
    return () => {
      cancelled = true;
    };
  }, [debounced, open]);

  const matchedCommands = useMemo(() => {
    const q = query.trim().toLowerCase().replace(/^\//, "");
    if (!q) return COMMANDS.slice(0, 6);
    return COMMANDS.filter(
      (c) => c.label.toLowerCase().includes(q) || c.keywords.includes(q),
    ).slice(0, 6);
  }, [query]);

  // One flat list so arrow keys move through commands and monitors uniformly.
  const items = useMemo(
    () => [
      ...matchedCommands.map((c) => ({ kind: "command" as const, cmd: c })),
      ...monitors.map((m) => ({ kind: "monitor" as const, monitor: m })),
    ],
    [matchedCommands, monitors],
  );

  useEffect(() => {
    if (cursor >= items.length) setCursor(Math.max(0, items.length - 1));
  }, [items.length, cursor]);

  function activate(index: number) {
    const item = items[index];
    if (!item) return;
    onClose();
    if (item.kind === "command") navigate(item.cmd.to);
    else navigate(`/monitor/${item.monitor.id}`);
  }

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-[60] flex items-start justify-center bg-slate-900/40 pt-[12vh]"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search and commands"
        className="w-full max-w-xl overflow-hidden rounded-lg bg-white shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-slate-200 px-3">
          <Search className="size-4 shrink-0 text-slate-400" aria-hidden="true" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setCursor(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setCursor((c) => Math.min(c + 1, items.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setCursor((c) => Math.max(c - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                activate(cursor);
              } else if (e.key === "Escape") {
                onClose();
              }
            }}
            placeholder="Search monitors, or type / for commands"
            className="w-full py-3 text-sm outline-none placeholder:text-slate-400"
          />
          <kbd className="shrink-0 rounded border border-slate-300 px-1.5 py-0.5 text-[10px] text-slate-500">
            esc
          </kbd>
        </div>

        <div className="max-h-[55vh] overflow-y-auto scroll-thin">
          {matchedCommands.length > 0 && (
            <Section label={query.startsWith("/") ? "Commands" : "Go to"}>
              {matchedCommands.map((c, i) => {
                const Icon = c.icon;
                return (
                  <Row
                    key={c.id}
                    active={cursor === i}
                    onHover={() => setCursor(i)}
                    onClick={() => activate(i)}
                  >
                    <Icon className="size-4 shrink-0 text-slate-400" aria-hidden="true" />
                    <span className="text-[13px] text-slate-800">{c.label}</span>
                    {c.hint && <span className="truncate text-[11px] text-slate-500">{c.hint}</span>}
                    <ArrowRight className="ml-auto size-3.5 shrink-0 text-slate-300" aria-hidden="true" />
                  </Row>
                );
              })}
            </Section>
          )}

          {monitors.length > 0 && (
            <Section label="Monitors">
              {monitors.map((m, i) => {
                const idx = matchedCommands.length + i;
                const st = STATUS_STYLE[m.status];
                return (
                  <Row
                    key={m.id}
                    active={cursor === idx}
                    onHover={() => setCursor(idx)}
                    onClick={() => activate(idx)}
                  >
                    <span className={cx("size-2 shrink-0 rounded-full", st.dot)} aria-hidden="true" />
                    <span className="truncate text-[13px] text-slate-800">{m.display_name}</span>
                    <span className="shrink-0 text-[11px] text-slate-500">{m.type_name}</span>
                    <span className={cx("ml-auto shrink-0 text-[11px]", st.text)}>
                      {STATUS_LABEL[m.status]}
                    </span>
                  </Row>
                );
              })}
            </Section>
          )}

          {query.trim().length >= 2 && !query.startsWith("/") && !searching && monitors.length === 0 && (
            <p className="px-4 py-6 text-center text-[13px] text-slate-500">
              No monitor matches “{query.trim()}”.
            </p>
          )}
          {searching && (
            <p className="px-4 py-3 text-center text-[12px] text-slate-400">Searching…</p>
          )}
        </div>

        <div className="flex items-center gap-3 border-t border-slate-200 bg-slate-50 px-3 py-2 text-[11px] text-slate-500">
          <span className="inline-flex items-center gap-1">
            <CornerDownLeft className="size-3" aria-hidden="true" /> open
          </span>
          <span>↑↓ navigate</span>
          <span className="ml-auto">
            <kbd className="rounded border border-slate-300 px-1">/</kbd> commands
          </span>
        </div>
      </div>
    </div>
  );
}

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="py-1">
      <div className="px-3 py-1 text-[10px] font-semibold tracking-wide text-slate-400 uppercase">
        {label}
      </div>
      {children}
    </div>
  );
}

function Row({
  active,
  onHover,
  onClick,
  children,
}: {
  active: boolean;
  onHover: () => void;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onMouseEnter={onHover}
      onClick={onClick}
      className={cx(
        "flex w-full items-center gap-2.5 px-3 py-2 text-left",
        active ? "bg-brand-50" : "hover:bg-slate-50",
      )}
    >
      {children}
    </button>
  );
}

/**
 * Global shortcut handling.
 *
 * "/" is ignored while focus is in a text field, or typing a search term
 * elsewhere would open the palette mid-word.
 */
export function usePaletteShortcut(onOpen: () => void) {
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const el = document.activeElement;
      const typing =
        el instanceof HTMLInputElement ||
        el instanceof HTMLTextAreaElement ||
        el instanceof HTMLSelectElement ||
        (el instanceof HTMLElement && el.isContentEditable);

      if ((e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpen();
        return;
      }
      if (e.key === "/" && !typing) {
        e.preventDefault();
        onOpen();
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [onOpen]);
}

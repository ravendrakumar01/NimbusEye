/**
 * Small presentational primitives.
 *
 * Hand-written rather than pulled from a component library: the set needed here
 * is narrow, and owning it avoids both a dependency and the licensing question
 * around copying another product's design assets. The structure and interaction
 * patterns follow common monitoring-console conventions; the styling is ours.
 */

import type { ReactNode } from "react";
import { AlertTriangle, ArrowUpDown, Inbox, Loader2, Menu } from "lucide-react";
import type { Severity, Status } from "../lib/api";
import {
  SEVERITY_AS_STATUS,
  SEVERITY_LABEL,
  STATUS_LABEL,
  STATUS_STYLE,
  cx,
  num,
} from "../lib/format";

export function Panel({
  title,
  subtitle,
  actions,
  children,
  className,
  bodyClassName,
}: {
  title?: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
}) {
  return (
    <section
      className={cx(
        "rounded-lg border border-slate-200 bg-white shadow-[0_1px_2px_rgba(15,23,42,0.06)]",
        className,
      )}
    >
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-slate-200 px-4 py-3">
          <div className="min-w-0">
            {title && <h2 className="truncate text-sm font-semibold text-slate-700">{title}</h2>}
            {subtitle && <p className="mt-0.5 truncate text-xs text-slate-500">{subtitle}</p>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={cx("p-4", bodyClassName)}>{children}</div>
    </section>
  );
}

/** A coloured dot. Always paired with a text label, never used on its own. */
export function StatusDot({ status, className }: { status: Status; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cx("inline-block size-2 shrink-0 rounded-full", STATUS_STYLE[status].dot, className)}
    />
  );
}

export function StatusBadge({ status, size = "sm" }: { status: Status; size?: "sm" | "xs" }) {
  const s = STATUS_STYLE[status];
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full font-medium ring-1",
        s.bg,
        s.text,
        s.ring,
        size === "xs" ? "px-1.5 py-0.5 text-[11px]" : "px-2 py-0.5 text-xs",
      )}
    >
      <StatusDot status={status} />
      {STATUS_LABEL[status]}
    </span>
  );
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  const status = SEVERITY_AS_STATUS[severity];
  const s = STATUS_STYLE[status];
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1",
        s.bg,
        s.text,
        s.ring,
      )}
    >
      <StatusDot status={status} />
      {SEVERITY_LABEL[severity]}
    </span>
  );
}

/**
 * A dashboard tile. Renders as a button when clickable, so filtering by clicking
 * a tile works with the keyboard too.
 */
export function StatTile({
  label,
  value,
  status,
  hint,
  active,
  onClick,
}: {
  label: string;
  value: number | string;
  status?: Status;
  hint?: string;
  active?: boolean;
  onClick?: () => void;
}) {
  const s = status ? STATUS_STYLE[status] : undefined;
  const body = (
    <>
      <div className="flex items-center gap-2">
        {status && <StatusDot status={status} />}
        <span className="truncate text-xs font-medium tracking-wide text-slate-500 uppercase">
          {label}
        </span>
      </div>
      <div className={cx("num mt-2 text-2xl font-semibold", s ? s.text : "text-slate-800")}>
        {typeof value === "number" ? num(value) : value}
      </div>
      {hint && <div className="mt-0.5 truncate text-xs text-slate-500">{hint}</div>}
    </>
  );

  const base = cx(
    "rounded-lg border bg-white px-4 py-3 text-left shadow-[0_1px_2px_rgba(15,23,42,0.06)] transition",
    active ? "border-brand-500 ring-1 ring-brand-500" : "border-slate-200",
    onClick && "hover:border-brand-400 hover:shadow-md cursor-pointer",
  );

  if (!onClick) return <div className={base}>{body}</div>;
  return (
    <button type="button" onClick={onClick} aria-pressed={active} className={cx(base, "w-full")}>
      {body}
    </button>
  );
}

/** Horizontal stacked bar showing a status breakdown. */
export function StatusBar({
  counts,
  total,
  className,
}: {
  counts: Partial<Record<Status, number>>;
  total: number;
  className?: string;
}) {
  const order: Status[] = ["down", "critical", "trouble", "unknown", "discovery", "maintenance", "suspended", "up"];
  return (
    <div
      className={cx("flex h-2 w-full overflow-hidden rounded-full bg-slate-100", className)}
      role="img"
      aria-label={order
        .filter((s) => counts[s])
        .map((s) => `${STATUS_LABEL[s]} ${counts[s]}`)
        .join(", ")}
    >
      {order.map((s) => {
        const n = counts[s] ?? 0;
        if (!n || total <= 0) return null;
        return (
          <span
            key={s}
            className={STATUS_STYLE[s].dot}
            style={{ width: `${(n / total) * 100}%` }}
            title={`${STATUS_LABEL[s]}: ${num(n)}`}
          />
        );
      })}
    </div>
  );
}

export function Button({
  children,
  onClick,
  variant = "default",
  size = "sm",
  disabled,
  type = "button",
  className,
}: {
  children: ReactNode;
  onClick?: () => void;
  variant?: "default" | "primary" | "ghost" | "danger";
  size?: "sm" | "xs";
  disabled?: boolean;
  type?: "button" | "submit";
  className?: string;
}) {
  const variants = {
    default: "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
    primary: "border-brand-600 bg-brand-600 text-white hover:bg-brand-700",
    ghost: "border-transparent bg-transparent text-slate-600 hover:bg-slate-100",
    danger: "border-st-down bg-white text-st-down hover:bg-st-down-bg",
  } as const;
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={cx(
        "inline-flex items-center gap-1.5 rounded-md border font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
        size === "xs" ? "px-2 py-1 text-xs" : "px-2.5 py-1.5 text-sm",
        variants[variant],
        className,
      )}
    >
      {children}
    </button>
  );
}

export function Select<T extends string>({
  value,
  onChange,
  options,
  placeholder,
  className,
  label,
}: {
  value: T | "";
  onChange: (v: T | "") => void;
  options: { value: T; label: string; count?: number }[];
  placeholder?: string;
  className?: string;
  label: string;
}) {
  return (
    <label className={cx("relative inline-flex", className)}>
      <span className="sr-only">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value as T | "")}
        className="w-full appearance-none rounded-md border border-slate-300 bg-white py-1.5 pr-7 pl-2.5 text-sm text-slate-700 hover:bg-slate-50"
      >
        <option value="">{placeholder ?? `All ${label.toLowerCase()}`}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
            {o.count !== undefined ? ` (${o.count})` : ""}
          </option>
        ))}
      </select>
      <svg
        aria-hidden="true"
        viewBox="0 0 20 20"
        className="pointer-events-none absolute top-2.5 right-2 size-3.5 fill-slate-400"
      >
        <path d="M5.5 7.5 10 12l4.5-4.5z" />
      </svg>
    </label>
  );
}

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-sm text-slate-500">
      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
      <span>{label}…</span>
    </div>
  );
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-12 text-center">
      <Inbox className="size-6 text-slate-300" aria-hidden="true" />
      <p className="text-sm font-medium text-slate-600">{title}</p>
      {hint && <p className="max-w-sm text-xs text-slate-500">{hint}</p>}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-col items-center justify-center gap-2 py-12 text-center"
    >
      <AlertTriangle className="size-6 text-st-down" aria-hidden="true" />
      <p className="text-sm font-medium text-slate-700">Could not load this data</p>
      <p className="max-w-md text-xs text-slate-500">{error.message}</p>
      {onRetry && (
        <Button onClick={onRetry} className="mt-1">
          Retry
        </Button>
      )}
    </div>
  );
}

export function Pagination({
  page,
  pageSize,
  total,
  onPage,
}: {
  page: number;
  pageSize: number;
  total: number;
  onPage: (p: number) => void;
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(total, page * pageSize);
  return (
    <div className="flex items-center justify-between gap-3 border-t border-slate-200 px-4 py-2.5 text-xs text-slate-600">
      <span className="num">
        {num(from)}–{num(to)} of {num(total)}
      </span>
      <div className="flex items-center gap-1.5">
        <Button size="xs" disabled={page <= 1} onClick={() => onPage(page - 1)}>
          Previous
        </Button>
        <span className="num px-1">
          {page} / {pages}
        </span>
        <Button size="xs" disabled={page >= pages} onClick={() => onPage(page + 1)}>
          Next
        </Button>
      </div>
    </div>
  );
}

/** Dependency-free sparkline. Enough for a table cell; real charts come later. */
export function Sparkline({
  values,
  className,
  tone = "stroke-brand-500",
}: {
  values: number[];
  className?: string;
  tone?: string;
}) {
  if (values.length < 2) return <span className="text-xs text-slate-400">—</span>;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const w = 80;
  const h = 20;
  const pts = values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * w;
      const y = h - ((v - min) / span) * h;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className={cx("h-5 w-20", className)} aria-hidden="true">
      <polyline points={pts} fill="none" strokeWidth="1.5" className={tone} />
    </svg>
  );
}

/* ------------------------------------------------------------------------ */
/* Page chrome and status visuals matching the reference console             */
/* ------------------------------------------------------------------------ */

/**
 * Page header: title on the left, actions on the right, thin bottom rule.
 * Sits directly on the white canvas rather than inside a card, as in the original.
 */
export function PageHeader({
  title,
  meta,
  actions,
}: {
  title: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex min-h-[46px] flex-wrap items-center gap-3 border-b border-slate-200 px-5 py-2">
      <h1 className="flex items-baseline gap-2 text-[17px] font-medium text-slate-800">{title}</h1>
      {meta && <span className="text-xs text-slate-500">{meta}</span>}
      {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
    </div>
  );
}

/**
 * Status ring: a thin circle with the count inside and the label beneath.
 *
 * A ring rather than a filled tile because the count, not the colour block, is
 * the thing being read; the colour only says which count it is.
 */
export function StatusRing({
  count,
  label,
  status,
  active,
  onClick,
}: {
  count: number;
  label: string;
  status: Status | "anomaly";
  active?: boolean;
  onClick?: () => void;
}) {
  const colorVar = status === "anomaly" ? "--color-st-anomaly" : `--color-st-${status}`;
  const body = (
    <>
      <span
        className="grid size-[58px] place-items-center rounded-full border-[3px] transition"
        style={{ borderColor: `var(${colorVar})` }}
      >
        <span className="num text-xl font-medium text-slate-800">{num(count)}</span>
      </span>
      <span className="mt-1.5 text-center text-xs text-slate-600">{label}</span>
    </>
  );
  const cls = cx(
    "flex flex-col items-center px-4 py-1",
    onClick && "cursor-pointer rounded hover:bg-slate-50",
    active && "rounded bg-slate-100",
  );
  if (!onClick) return <div className={cls}>{body}</div>;
  return (
    <button type="button" onClick={onClick} aria-pressed={active} className={cls}>
      {body}
    </button>
  );
}

/** Bordered card used for the status ring row and the counters beside it. */
export function Card({
  children,
  className,
  title,
  action,
}: {
  children: ReactNode;
  className?: string;
  title?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className={cx("rounded border border-slate-200 bg-white", className)}>
      {(title || action) && (
        <div className="flex items-center justify-between gap-2 px-3 pt-2.5">
          {title && <div className="text-[13px] font-medium text-brand-500">{title}</div>}
          {action}
        </div>
      )}
      {children}
    </div>
  );
}

/** Segmented pill group, as used for the polling window selector. */
export function PillGroup<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  label: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex overflow-hidden rounded border border-slate-300"
    >
      {options.map((o, i) => (
        <button
          key={o.value}
          type="button"
          onClick={() => onChange(o.value)}
          aria-pressed={value === o.value}
          className={cx(
            "px-2.5 py-1 text-xs transition",
            i > 0 && "border-l border-slate-300",
            value === o.value
              ? "bg-slate-100 font-medium text-slate-800"
              : "bg-white text-slate-600 hover:bg-slate-50",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** Sortable column header button. */
export function SortHeader({
  label,
  active,
  onClick,
  align = "left",
}: {
  label: ReactNode;
  active?: boolean;
  onClick?: () => void;
  align?: "left" | "right";
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cx(
        "inline-flex w-full items-center gap-1 text-[13px] font-medium",
        align === "right" ? "justify-end" : "justify-start",
        active ? "text-slate-900" : "text-slate-600 hover:text-slate-900",
      )}
    >
      <span>{label}</span>
      <ArrowUpDown className="size-3 text-slate-400" aria-hidden="true" />
    </button>
  );
}

/** Row action menu trigger. */
export function RowMenu({ label }: { label: string }) {
  return (
    <button
      type="button"
      aria-label={label}
      className="grid size-6 place-items-center rounded text-slate-400 hover:bg-slate-100 hover:text-slate-600"
    >
      <Menu className="size-4" aria-hidden="true" />
    </button>
  );
}

/**
 * Horizontal tab strip, active tab underlined. Sits inline with the page title
 * in the reference console rather than in a bar of its own.
 */
export function TabStrip<T extends string>({
  tabs,
  value,
  onChange,
  label,
}: {
  tabs: { value: T; label: string; count?: number }[];
  value: T;
  onChange: (v: T) => void;
  label: string;
}) {
  return (
    <div role="tablist" aria-label={label} className="flex items-center gap-1">
      {tabs.map((t) => {
        const active = t.value === value;
        return (
          <button
            key={t.value}
            role="tab"
            aria-selected={active}
            type="button"
            onClick={() => onChange(t.value)}
            className={cx(
              "border-b-2 px-3 py-1.5 text-[13px] transition",
              active
                ? "border-slate-800 font-medium text-slate-900"
                : "border-transparent text-slate-600 hover:text-slate-900",
            )}
          >
            {t.label}
            {t.count !== undefined && t.count > 0 && (
              <span className="num ml-1.5 rounded-full bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-600">
                {t.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/** Amber informational banner, as used for setup notes and warnings. */
export function InfoBanner({
  children,
  tone = "info",
}: {
  children: ReactNode;
  tone?: "info" | "warn";
}) {
  return (
    <div
      className={cx(
        "rounded border px-3 py-2 text-[13px]",
        tone === "warn"
          ? "border-st-critical/30 bg-st-critical-bg text-slate-700"
          : "border-amber-200 bg-amber-50 text-slate-700",
      )}
    >
      {children}
    </div>
  );
}

/** Small key/value row used by the inventory panels. */
export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex gap-3 border-b border-slate-100 py-2 last:border-0">
      <dt className="w-44 shrink-0 text-[12px] text-slate-500">{label}</dt>
      <dd className="min-w-0 flex-1 text-[13px] break-words text-slate-800">{children}</dd>
    </div>
  );
}

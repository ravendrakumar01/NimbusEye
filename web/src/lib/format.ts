/** Display formatting helpers, plus the status vocabulary the UI renders. */

import type { Severity, Status } from "./api";

/** Human labels. Kept out of the components so wording changes in one place. */
export const STATUS_LABEL: Record<Status, string> = {
  down: "Down",
  critical: "Critical",
  trouble: "Trouble",
  up: "Up",
  maintenance: "Maintenance",
  suspended: "Suspended",
  discovery: "Discovery",
  unknown: "Unknown",
};

/**
 * Tailwind classes per status. Severity is conveyed by colour *and* by the label
 * and glyph, so the interface stays readable without colour perception.
 */
export const STATUS_STYLE: Record<Status, { text: string; bg: string; dot: string; ring: string }> = {
  down: { text: "text-st-down", bg: "bg-st-down-bg", dot: "bg-st-down", ring: "ring-st-down/30" },
  critical: {
    text: "text-st-critical",
    bg: "bg-st-critical-bg",
    dot: "bg-st-critical",
    ring: "ring-st-critical/30",
  },
  trouble: {
    text: "text-st-trouble",
    bg: "bg-st-trouble-bg",
    dot: "bg-st-trouble",
    ring: "ring-st-trouble/30",
  },
  up: { text: "text-st-up", bg: "bg-st-up-bg", dot: "bg-st-up", ring: "ring-st-up/30" },
  maintenance: {
    text: "text-st-maintenance",
    bg: "bg-st-maintenance-bg",
    dot: "bg-st-maintenance",
    ring: "ring-st-maintenance/30",
  },
  suspended: {
    text: "text-st-suspended",
    bg: "bg-st-suspended-bg",
    dot: "bg-st-suspended",
    ring: "ring-st-suspended/30",
  },
  discovery: {
    text: "text-st-discovery",
    bg: "bg-st-discovery-bg",
    dot: "bg-st-discovery",
    ring: "ring-st-discovery/30",
  },
  unknown: {
    text: "text-st-unknown",
    bg: "bg-st-unknown-bg",
    dot: "bg-st-unknown",
    ring: "ring-st-unknown/30",
  },
};

/** Worst first. Used for tile order and for sorting. */
export const STATUS_ORDER: Status[] = [
  "down",
  "critical",
  "trouble",
  "up",
  "maintenance",
  "discovery",
  "suspended",
  "unknown",
];

export const SEVERITY_LABEL: Record<Severity, string> = {
  down: "Down",
  critical: "Critical",
  trouble: "Trouble",
  info: "Info",
};

export const SEVERITY_AS_STATUS: Record<Severity, Status> = {
  down: "down",
  critical: "critical",
  trouble: "trouble",
  info: "unknown",
};

export const PROVIDER_LABEL: Record<string, string> = {
  oci: "Oracle Cloud",
  aws: "AWS",
  azure: "Azure",
  gcp: "Google Cloud",
  k8s: "Kubernetes",
  synthetic: "Website",
};

export const CATEGORY_LABEL: Record<string, string> = {
  compute: "Compute",
  storage: "Storage",
  database: "Database",
  network: "Network",
  container: "Container",
  serverless: "Serverless",
  web: "Web",
};

/** 1234567 -> "1,234,567" */
export function num(n: number): string {
  return n.toLocaleString("en-IN");
}

/** Percentage with a sensible number of digits: 100 -> "100%", 99.512 -> "99.51%" */
export function pct(v: number, digits = 2): string {
  if (!Number.isFinite(v)) return "—";
  if (v === 0) return "0%";
  if (v >= 100) return "100%";
  return `${v.toFixed(digits)}%`;
}

export function bytes(v: number): string {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  let x = v;
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024;
    i++;
  }
  return `${x.toFixed(x < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

/** Formats a metric value according to its declared unit. */
export function metricValue(v: number | undefined, unit: string | undefined): string {
  if (v === undefined || v === null || !Number.isFinite(v)) return "—";
  switch (unit) {
    case "percent":
      return `${v.toFixed(v < 10 ? 1 : 0)}%`;
    case "bytes":
      return bytes(v);
    case "bytes_per_sec":
      return `${bytes(v)}/s`;
    case "milliseconds":
      return v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${v.toFixed(0)} ms`;
    case "seconds":
      return v < 1 ? `${(v * 1000).toFixed(0)} ms` : `${v.toFixed(2)} s`;
    case "ops_per_sec":
      return `${num(Math.round(v))}/s`;
    default:
      return num(Math.round(v * 100) / 100);
  }
}

/** "3d 4h", "12m", "just now" — compact enough for a dense table column. */
export function duration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  if (s < 45) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) {
    const rm = m % 60;
    return rm ? `${h}h ${rm}m` : `${h}h`;
  }
  const d = Math.floor(h / 24);
  const rh = h % 24;
  return rh ? `${d}d ${rh}h` : `${d}d`;
}

/** How long ago an ISO timestamp was. */
export function since(iso: string | null | undefined): string {
  if (!iso) return "never";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  return duration((Date.now() - t) / 1000);
}

/** Absolute local time for tooltips, where precision matters more than brevity. */
export function absolute(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("en-IN", {
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/**
 * Availability colour thresholds. Deliberately strict: 99.9% is one nine short of
 * a common target, so anything below it should not read as green.
 */
export function availabilityTone(v: number): string {
  if (v === 0) return "text-slate-400";
  if (v >= 99.9) return "text-st-up";
  if (v >= 99) return "text-st-trouble";
  return "text-st-down";
}

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

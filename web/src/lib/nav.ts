/**
 * Navigation model.
 *
 * The console has two levels of chrome: a narrow icon rail that selects a
 * domain, and a context panel whose contents change with the selected rail item.
 * Keeping both in one data structure means the shell renders generically and
 * adding a section is a data change, not a component change.
 */

import {
  Activity,
  BellRing,
  Boxes,
  Cloud,
  Cog,
  FileText,
  Globe,
  Home as HomeIcon,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

/** One row in the context panel. */
export interface PanelItem {
  label: string;
  /** Route to navigate to. Omitted for group headers and not-yet-built rows. */
  to?: string;
  /** Renders a "+" affordance on the right, as the original does for creatable types. */
  add?: boolean;
  /** Indented under the group above it. */
  indent?: boolean;
  /** Collapsible group header. */
  group?: boolean;
  /** Initial expanded state for a group. */
  open?: boolean;
  /** Small badge, e.g. "New" or a count. */
  badge?: string;
  /** Renders greyed out: visible in the original but not implemented here. */
  stub?: boolean;
  /**
   * Why it is not built. Shown on hover.
   *
   * A greyed row with no explanation reads as a bug or a permissions problem.
   * Stating the reason turns it into a known boundary, which is the difference
   * between an unfinished product and an untrustworthy one.
   */
  stubReason?: string;
}

export interface RailSection {
  key: string;
  label: string;
  icon: LucideIcon;
  /** Route the rail item itself navigates to. */
  to: string;
  /** Heading shown at the top of the context panel, if any. */
  panelTitle?: string;
  items: PanelItem[];
  /** Cloud shows a provider tab strip above its panel items. */
  providerTabs?: { key: string; label: string }[];
}

export const RAIL: RailSection[] = [
  {
    key: "home",
    label: "Home",
    icon: HomeIcon,
    to: "/",
    items: [
      // Only what exists. The unbuilt rows the reference console shows are listed
      // in the README instead of being greyed out here — a console full of dead
      // entries reads as broken rather than as unfinished, and this one is in
      // daily use.
      { label: "Monitors", to: "/", add: true },
      { label: "Monitor Groups", to: "/groups", add: true },
      { label: "Outages", to: "/outages" },
      { label: "Schedule Maintenance", to: "/maintenance" },
      { label: "Alert Logs", to: "/alert-logs" },
      { label: "SLO", to: "/slo", add: true },
    ],
  },
  {
    key: "alarms",
    label: "Alarms",
    icon: BellRing,
    to: "/alarms",
    panelTitle: "Infrastructure Events",
    items: [],
  },
  {
    key: "web",
    label: "Web",
    icon: Globe,
    to: "/web",
    items: [
      // Only the check types the prober actually performs. The reference console
      // lists browser-based checks too; those need a headless browser fleet, which
      // is infrastructure rather than code, so they are absent instead of greyed.
      { label: "Discovered Resources", to: "/discovered" },
      { label: "Website", to: "/add-monitor?type=WEB_HTTP", add: true },
      { label: "REST API", to: "/web?type=WEB_REST_API" },
      { label: "Port (Custom Protocol)", to: "/web?type=WEB_PORT" },
      { label: "DNS Server", to: "/web?type=WEB_DNS" },
      { label: "SSL/TLS Certificate", to: "/web?type=WEB_SSL_CERT" },
      { label: "Domain Expiry", to: "/web?type=WEB_DOMAIN_EXPIRY" },
      { label: "PING", to: "/web?type=WEB_PING" },
    ],
  },
  {
    key: "k8s",
    label: "K8s",
    icon: Boxes,
    to: "/kubernetes",
    items: [
      { label: "Clusters", to: "/kubernetes" },
      { label: "What is monitored", to: "/kubernetes/help" },
    ],
  },
  {
    key: "cloud",
    label: "Cloud",
    icon: Cloud,
    to: "/cloud",
    providerTabs: [
      { key: "aws", label: "AWS" },
      { key: "azure", label: "Azure" },
      { key: "gcp", label: "GCP" },
      { key: "oci", label: "OCI" },
      { key: "more", label: "More" },
    ],
    items: [],
  },
  {
    key: "reports",
    label: "Reports",
    icon: FileText,
    to: "/reports",
    items: [
      { label: "Availability Summary", to: "/reports?tab=availability" },
      { label: "Performance Report", to: "/reports?tab=performance" },
      { label: "Health Trend", to: "/reports?tab=trend" },
      { label: "Outage Report", to: "/reports?tab=outages" },
      { label: "SLA Report", to: "/reports?tab=sla" },
      { label: "Top N / Bottom N", to: "/reports?tab=performance" },
    ],
  },
  {
    key: "admin",
    label: "Admin",
    icon: Cog,
    to: "/admin",
    items: [
      { label: "Inventory", group: true, open: true },
      { label: "Add Monitor", to: "/add-monitor", indent: true },
      { label: "Monitor Groups", to: "/groups", add: true, indent: true },
      { label: "Bulk Action", to: "/admin/bulk", indent: true },
      { label: "Cloud Accounts", to: "/admin/cloud-accounts" },
      { label: "User & Alert Management", group: true, open: true },
      { label: "Users", to: "/admin/users", add: true, indent: true },
      { label: "Notification Channels", to: "/admin/channels", add: true, indent: true },
      { label: "Audit Log", to: "/admin/audit", indent: true },
      { label: "Configuration Profiles", group: true, open: true },
      { label: "Threshold Profiles", to: "/admin/thresholds", indent: true },
      { label: "Notification Profiles", to: "/admin/notifications", indent: true },
      { label: "Business Hours", to: "/admin/business-hours", add: true, indent: true },
      { label: "Tags", to: "/admin/tags", indent: true },
    ],
  },
];

/** The "Edit" affordance that sits at the bottom of the rail in the original. */
export const RAIL_FOOTER = { label: "Edit", icon: Activity };

/** Resolves which rail section a pathname belongs to. */
export function sectionForPath(pathname: string): RailSection {
  // Longest matching prefix wins, so /kubernetes/help resolves to the K8s rail.
  const match = [...RAIL]
    .filter((s) => s.to !== "/" && pathname.startsWith(s.to))
    .sort((a, b) => b.to.length - a.to.length)[0];
  return match ?? RAIL[0]!;
}

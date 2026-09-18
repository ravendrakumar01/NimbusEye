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
  Gauge,
  Globe,
  HardDrive,
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
      { label: "Getting Started", to: "/getting-started" },
      { label: "Dashboards", stub: true },
      { label: "Monitors", to: "/", add: true },
      { label: "Add Monitor", to: "/add-monitor" },
      { label: "Monitor Groups", to: "/groups", add: true },
      { label: "Capacity Planning", add: true, stub: true },
      { label: "Outages", to: "/outages" },
      { label: "Zia Anomaly Dashboard", stub: true },
      { label: "Schedule Maintenance", stub: true },
      { label: "Schedule IT Automation", stub: true },
      { label: "Log Report", stub: true },
      { label: "Alert Logs", stub: true },
      { label: "IT Automation Logs", stub: true },
      { label: "SLO", add: true, stub: true },
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
      { label: "Help Assistant", stub: true },
      { label: "Discovered Resources", stub: true },
      { label: "Website", to: "/add-monitor?type=WEB_HTTP", add: true },
      { label: "Web Transaction (Browser)", add: true, stub: true },
      { label: "Synthetic Mobile App", add: true, stub: true },
      { label: "API", group: true, open: true },
      { label: "REST API", to: "/web?type=WEB_REST_API", add: true, indent: true },
      { label: "SOAP Web Service", add: true, indent: true, stub: true },
      { label: "gRPC", add: true, indent: true, stub: true },
      { label: "REST API Transaction", add: true, indent: true, stub: true },
      { label: "File Upload", add: true, indent: true, stub: true },
      { label: "Application Cluster", add: true, stub: true },
      { label: "SaaS Synthetics (Browser)", add: true, stub: true },
      { label: "WebSocket", add: true, stub: true },
      { label: "Webpage Speed (Browser)", add: true, stub: true },
      { label: "Mail Delivery", add: true, stub: true },
      { label: "Port (Custom Protocol)", to: "/web?type=WEB_PORT", add: true },
      { label: "UDP Port", add: true, stub: true },
      { label: "DNS Server", to: "/web?type=WEB_DNS", add: true },
      { label: "SSL/TLS Certificate", to: "/web?type=WEB_SSL_CERT", add: true },
      { label: "Domain Expiry", to: "/web?type=WEB_DOMAIN_EXPIRY", add: true },
      { label: "PING", to: "/web?type=WEB_PING", add: true },
      { label: "Heartbeat", add: true, stub: true },
    ],
  },
  {
    key: "apm",
    label: "APM",
    icon: Gauge,
    to: "/apm",
    items: [
      { label: "Help Assistant", stub: true },
      { label: "Applications", stub: true },
      { label: "Real User Monitoring", stub: true },
      { label: "Mobile APM", stub: true },
      { label: "Traces", stub: true },
    ],
  },
  {
    key: "server",
    label: "Server",
    icon: HardDrive,
    to: "/server",
    items: [
      { label: "Help Assistant", stub: true },
      { label: "Servers", stub: true },
      { label: "Plugins", stub: true },
      { label: "Docker", stub: true },
      { label: "AppLogs", stub: true },
    ],
  },
  {
    key: "k8s",
    label: "K8s",
    icon: Boxes,
    to: "/kubernetes",
    items: [
      { label: "Help Assistant", to: "/kubernetes/help" },
      { label: "Clusters", to: "/kubernetes" },
      { label: "Add Kubernetes Monitor", stub: true },
      { label: "Kubernetes Change Tracker", badge: "New", stub: true },
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
      { label: "Outage Report", to: "/reports?tab=outages" },
      { label: "SLA Report", to: "/reports?tab=sla" },
      { label: "Top N / Bottom N", to: "/reports?tab=performance" },
      { label: "Health Trend", stub: true },
      { label: "Forecast Report", stub: true },
      { label: "Custom Reports", stub: true },
      { label: "Schedule Reports", stub: true },
    ],
  },
  {
    key: "admin",
    label: "Admin",
    icon: Cog,
    to: "/admin",
    items: [
      { label: "Help Assistant", stub: true },
      { label: "Inventory", group: true, open: true },
      { label: "Add Monitor", to: "/add-monitor", indent: true },
      { label: "Monitors", add: true, indent: true, stub: true },
      { label: "Monitor Groups", add: true, indent: true, stub: true },
      { label: "Smart Groups", indent: true, stub: true },
      { label: "Import Monitors", indent: true, stub: true },
      { label: "Configuration Rules", add: true, indent: true, stub: true },
      { label: "Bulk Action", indent: true, stub: true },
      { label: "Cloud Accounts", to: "/admin/cloud-accounts" },
      { label: "User & Alert Management", group: true, open: true },
      { label: "Users", to: "/admin/users", add: true, indent: true },
      { label: "User Groups", add: true, indent: true, stub: true },
      { label: "On-Call Schedules", add: true, indent: true, stub: true },
      { label: "Notification Channels", to: "/admin/channels", add: true, indent: true },
      { label: "Audit Log", to: "/admin/audit", indent: true },
      { label: "Configuration Profiles", group: true, open: true },
      { label: "Threshold Profiles", to: "/admin/thresholds", indent: true },
      { label: "Notification Profiles", to: "/admin/notifications", indent: true },
      { label: "Business Hours", add: true, indent: true, stub: true },
      { label: "Tags", add: true, indent: true, stub: true },
      { label: "Automations", group: true },
      { label: "Server Monitor", group: true },
      { label: "AppLogs", group: true },
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

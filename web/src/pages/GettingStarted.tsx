/**
 * Getting Started — the post-login landing page.
 *
 * Follows the reference console's onboarding page: a welcome, then one card per
 * monitoring domain with the quickest way into each. Kept as the landing page when
 * the estate is empty, because "what do I do first" is the only question that
 * matters at that moment, and an empty monitor list answers it badly.
 *
 * Cards for things NimbusEye cannot do yet are shown rather than hidden, and are
 * labelled. The alternative — a short page listing only what works — hides the
 * shape of the product and makes the gaps invisible until someone goes looking.
 */

import { Link } from "react-router-dom";
import {
  Activity,
  Boxes,
  Cloud,
  Database,
  FileText,
  Gauge,
  Globe,
  Info,
  Lock,
  Network,
  Plug,
  Server,
} from "lucide-react";
import { api } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { cx, num } from "../lib/format";
import { Card, PageHeader } from "../components/ui";

interface Action {
  label: string;
  to?: string;
}

interface Offering {
  title: string;
  icon: typeof Globe;
  tone: string;
  description: string;
  actions: Action[];
  /** Set when the capability does not exist yet; the reason is shown. */
  unavailable?: string;
}

interface Group {
  heading: string;
  items: Offering[];
}

const GROUPS: Group[] = [
  {
    heading: "End User Experience Monitoring",
    items: [
      {
        title: "Website",
        icon: Globe,
        tone: "text-brand-500",
        description:
          "Check that a URL responds, how fast, and that the page contains what it should. Catches a site that returns 200 while being broken.",
        actions: [
          { label: "Website", to: "/add-monitor?type=WEB_HTTP" },
          { label: "REST API", to: "/add-monitor?type=WEB_REST_API" },
        ],
      },
      {
        title: "Certificates & Domains",
        icon: Lock,
        tone: "text-st-up",
        description:
          "Watch TLS certificate and domain registration expiry so neither lapses unnoticed. Read off the wire, not from a calendar.",
        actions: [
          { label: "SSL Certificate", to: "/add-monitor?type=WEB_SSL_CERT" },
          { label: "Domain Expiry", to: "/add-monitor?type=WEB_DOMAIN_EXPIRY" },
        ],
      },
      {
        title: "Synthetic Transactions",
        icon: Activity,
        tone: "text-st-discovery",
        description:
          "Step-by-step interactions through a real browser, to find broken or slow steps in a login or checkout flow.",
        actions: [{ label: "Record Transaction" }],
        unavailable: "Needs a headless browser fleet, which is infrastructure rather than code.",
      },
    ],
  },
  {
    heading: "Infrastructure Monitoring",
    items: [
      {
        title: "Public Cloud",
        icon: Cloud,
        tone: "text-warn-500",
        description:
          "Discover and monitor a whole tenancy. Resources are found automatically, with read-only credentials.",
        actions: [
          { label: "OCI", to: "/admin/cloud-accounts" },
          { label: "AWS", to: "/admin/cloud-accounts" },
          { label: "Azure", to: "/admin/cloud-accounts" },
          { label: "GCP", to: "/admin/cloud-accounts" },
        ],
      },
      {
        title: "Network Checks",
        icon: Network,
        tone: "text-st-maintenance",
        description:
          "TCP port reachability, DNS resolution with answer assertion, and ICMP. Catches stale or hijacked DNS records.",
        actions: [
          { label: "Port / TCP", to: "/add-monitor?type=WEB_PORT" },
          { label: "DNS", to: "/add-monitor?type=WEB_DNS" },
          { label: "PING", to: "/add-monitor?type=WEB_PING" },
        ],
      },
      {
        title: "Server",
        icon: Server,
        tone: "text-slate-500",
        description:
          "Agent-based host monitoring: CPU, memory, disk, processes and services on Linux and Windows.",
        actions: [{ label: "Server" }, { label: "Kubernetes" }],
        unavailable: "Needs an agent build and an install flow, separate from cloud API polling.",
      },
    ],
  },
  {
    heading: "Application & Data",
    items: [
      {
        title: "APM Insight",
        icon: Gauge,
        tone: "text-st-critical",
        description:
          "Application performance: traces, slow transactions and errors for Java, .NET, Node.js, Python and Go.",
        actions: [{ label: "Add Monitor" }],
        unavailable: "Needs per-language agents and a trace store.",
      },
      {
        title: "Databases",
        icon: Database,
        tone: "text-brand-600",
        description:
          "Autonomous Database and DB System metrics arrive with cloud discovery. Direct database monitoring is separate.",
        actions: [{ label: "Connect a cloud account", to: "/admin/cloud-accounts" }],
      },
      {
        title: "Kubernetes",
        icon: Boxes,
        tone: "text-st-discovery",
        description:
          "Cluster, node and workload health. OKE clusters are discovered with the tenancy; in-cluster detail needs an agent.",
        actions: [{ label: "View clusters", to: "/kubernetes" }],
      },
    ],
  },
  {
    heading: "Integrations & Logs",
    items: [
      {
        title: "Heartbeat",
        icon: Activity,
        tone: "text-st-up",
        description:
          "An inbound check: a scheduled job calls NimbusEye, and a missed call raises the alarm. Good for backups and cron.",
        actions: [{ label: "Heartbeat", to: "/add-monitor?type=WEB_HEARTBEAT" }],
      },
      {
        title: "Plugins",
        icon: Plug,
        tone: "text-slate-500",
        description: "Monitor anything by shipping a small script that reports its own metrics.",
        actions: [{ label: "Add Monitor" }],
        unavailable: "Needs a plugin protocol and an agent to run them.",
      },
      {
        title: "AppLogs",
        icon: FileText,
        tone: "text-slate-500",
        description: "Collect and search logs, with metrics extracted from them.",
        actions: [{ label: "Add Logs" }],
        unavailable: "Needs a log pipeline and a store built for it.",
      },
    ],
  },
];

export function GettingStarted() {
  const summary = useAsync(() => api.summary(), []);
  const accounts = useAsync(() => api.accounts(), []);

  const monitors = summary.data?.total ?? 0;
  const connected = accounts.data?.items.length ?? 0;

  return (
    <>
      <PageHeader
        title="Getting Started"
        actions={
          <Link to="/" className="text-[12px] text-brand-500 hover:underline">
            Go to Monitor Status →
          </Link>
        }
      />

      <div className="px-5 py-6">
        <div className="mb-7 text-center">
          <h1 className="text-[26px] font-medium text-slate-800">
            Welcome to <span className="text-go-500">NimbusEye</span>
          </h1>
          <p className="mt-1.5 text-[14px] text-slate-600">
            Choose what you want to monitor and get started.
          </p>
          {(monitors > 0 || connected > 0) && (
            <p className="num mt-3 text-[12px] text-slate-500">
              {num(monitors)} monitor{monitors === 1 ? "" : "s"}
              {connected > 0 && <> · {num(connected)} cloud account{connected === 1 ? "" : "s"} connected</>}
            </p>
          )}
        </div>

        <div className="mx-auto max-w-[1180px] space-y-7">
          {GROUPS.map((g) => (
            <section key={g.heading}>
              <h2 className="mb-3 text-[15px] font-medium text-slate-800">{g.heading}</h2>
              <div className="grid gap-4 lg:grid-cols-3">
                {g.items.map((o) => (
                  <OfferingCard key={o.title} offering={o} />
                ))}
              </div>
            </section>
          ))}
        </div>

        <div className="mx-auto mt-7 flex max-w-[1180px] flex-wrap items-center justify-between gap-3">
          <Link to="/add-monitor" className="text-[13px] text-brand-500 hover:underline">
            Show all monitor types
          </Link>
          <span className="inline-flex items-center gap-1.5 text-[12px] text-slate-500">
            <Info className="size-3.5 text-slate-400" aria-hidden="true" />
            Not the right person setting this up? User management is not built yet.
          </span>
        </div>
      </div>
    </>
  );
}

function OfferingCard({ offering: o }: { offering: Offering }) {
  const Icon = o.icon;
  const disabled = Boolean(o.unavailable);

  return (
    <Card className={cx("flex h-full flex-col px-4 py-4", disabled && "bg-slate-50/60")}>
      <div className="flex items-center gap-2.5">
        <Icon className={cx("size-5 shrink-0", disabled ? "text-slate-400" : o.tone)} aria-hidden="true" />
        <h3 className={cx("text-[14px] font-medium", disabled ? "text-slate-500" : "text-slate-800")}>
          {o.title}
        </h3>
        {disabled && (
          <span className="ml-auto rounded-full bg-slate-200 px-2 py-0.5 text-[10px] font-medium text-slate-600">
            not built
          </span>
        )}
      </div>

      <p className="mt-2 flex-1 text-[12.5px] leading-relaxed text-slate-600">{o.description}</p>

      {o.unavailable && <p className="mt-2 text-[11px] text-slate-500">{o.unavailable}</p>}

      <div className="mt-3 flex flex-wrap gap-2">
        {o.actions.map((a) =>
          a.to ? (
            <Link
              key={a.label}
              to={a.to}
              className="rounded border border-slate-300 bg-white px-2.5 py-1 text-[12px] text-slate-700 transition hover:border-brand-400 hover:text-brand-600"
            >
              {a.label}
            </Link>
          ) : (
            <span
              key={a.label}
              title={o.unavailable}
              className="cursor-not-allowed rounded border border-slate-200 px-2.5 py-1 text-[12px] text-slate-400"
            >
              {a.label}
            </span>
          ),
        )}
      </div>
    </Card>
  );
}

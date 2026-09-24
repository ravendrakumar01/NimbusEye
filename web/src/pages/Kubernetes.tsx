/**
 * Kubernetes clusters.
 *
 * Replaces a generic monitor list, which for a cluster showed a name and a status
 * dot. That is a monitor list, not a Kubernetes view: the questions somebody opens
 * this section to answer are what version is running, whether an upgrade is waiting,
 * how many nodes there are and whether any of them is unhealthy — and none of those
 * come from the metrics.
 *
 * The flat list is still reachable through Cloud Resources for anyone who wants the
 * inventory view.
 */

import { Link } from "react-router-dom";
import { AlertTriangle, ArrowUpCircle } from "lucide-react";

import { api } from "../lib/api";
import type { Resource } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { cx, num } from "../lib/format";
import {
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
  StatTile,
  StatusDot,
} from "../components/ui";

interface NodeState {
  name: string;
  state: string;
  version?: string;
  availability_domain?: string;
  private_ip?: string;
  error?: string;
}

interface NodePool {
  name: string;
  state: string;
  version?: string;
  shape?: string;
  ocpus?: number;
  memory_gb?: number;
  desired: number;
  active: number;
  pending: number;
  failed: number;
  nodes?: NodeState[];
}

/** Reads the collector's enrichment off a cluster resource. */
function clusterDetail(r: Resource) {
  const a = r.attributes ?? {};
  return {
    version: a.k8s_version as string | undefined,
    upgrades: (a.available_upgrades as string[] | undefined) ?? [],
    endpoint: a.endpoint as string | undefined,
    compartment: a.compartment_name as string | undefined,
    totalNodes: Number(a.total_nodes ?? 0),
    activeNodes: Number(a.active_nodes ?? 0),
    pendingNodes: Number(a.pending_nodes ?? 0),
    failedNodes: Number(a.unhealthy_nodes ?? 0),
    pools: (a.node_pools as NodePool[] | undefined) ?? [],
  };
}

/** Node counts grouped by availability domain, which is how a pool's spread reads. */
function byDomain(pools: NodePool[]): { ad: string; count: number }[] {
  const m = new Map<string, number>();
  for (const p of pools) {
    for (const n of p.nodes ?? []) {
      // OCI prefixes the domain with a tenancy-specific string; the tail is the
      // part that identifies it.
      const ad = (n.availability_domain ?? "unknown").split(":").pop() ?? "unknown";
      m.set(ad, (m.get(ad) ?? 0) + 1);
    }
  }
  return [...m.entries()].map(([ad, count]) => ({ ad, count })).sort((a, b) => a.ad.localeCompare(b.ad));
}

function ClusterCard({ cluster }: { cluster: Resource }) {
  const d = clusterDetail(cluster);
  const domains = byDomain(d.pools);
  const capacity = d.pools.reduce(
    (acc, p) => ({
      ocpus: acc.ocpus + (p.ocpus ?? 0) * p.active,
      memory: acc.memory + (p.memory_gb ?? 0) * p.active,
    }),
    { ocpus: 0, memory: 0 },
  );

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <StatusDot status={cluster.status} />
          <Link to={`/monitor/${cluster.id}`} className="hover:underline">
            {cluster.display_name}
          </Link>
          {d.compartment && (
            <span className="text-[11px] font-normal text-slate-400">{d.compartment}</span>
          )}
        </span>
      }
      action={
        d.version ? (
          <span className="flex items-center gap-2">
            <span className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[12px] text-slate-700">
              {d.version}
            </span>
            {d.upgrades.length > 0 && (
              <span
                className="inline-flex items-center gap-1 rounded bg-st-trouble-bg px-1.5 py-0.5 text-[11px] font-medium text-st-trouble"
                title={`Available: ${d.upgrades.join(", ")}`}
              >
                <ArrowUpCircle className="size-3" aria-hidden="true" />
                {d.upgrades[0]} available
              </span>
            )}
          </span>
        ) : undefined
      }
    >
      <div className="grid grid-cols-2 gap-x-6 gap-y-2 px-3 pt-2 pb-3 text-[13px] sm:grid-cols-4">
        <div>
          <div className="text-[11px] tracking-wide text-slate-500 uppercase">Nodes</div>
          <div className="mt-0.5 flex items-baseline gap-1.5">
            <span className="text-[18px] font-semibold text-slate-900 tabular-nums">
              {d.activeNodes}
            </span>
            {d.totalNodes !== d.activeNodes && (
              <span className="text-[12px] text-slate-500">of {d.totalNodes}</span>
            )}
          </div>
        </div>
        <div>
          <div className="text-[11px] tracking-wide text-slate-500 uppercase">Node pools</div>
          <div className="mt-0.5 text-[18px] font-semibold text-slate-900 tabular-nums">
            {d.pools.length}
          </div>
        </div>
        <div>
          <div className="text-[11px] tracking-wide text-slate-500 uppercase">Capacity</div>
          <div className="mt-0.5 text-[13px] text-slate-700">
            {capacity.ocpus ? `${capacity.ocpus} OCPU · ${capacity.memory} GB` : "—"}
          </div>
        </div>
        <div>
          <div className="text-[11px] tracking-wide text-slate-500 uppercase">Spread</div>
          <div className="mt-0.5 text-[12px] text-slate-600">
            {domains.length > 0
              ? domains.map((x) => `${x.ad.replace(/^.*-/, "AD-")} ${x.count}`).join(", ")
              : "—"}
          </div>
        </div>
      </div>

      {(d.failedNodes > 0 || d.pendingNodes > 0) && (
        <div className="px-3 pb-3">
          <InfoBanner tone={d.failedNodes > 0 ? "warn" : "info"}>
            {d.failedNodes > 0
              ? `${d.failedNodes} node${d.failedNodes === 1 ? "" : "s"} not healthy.`
              : `${d.pendingNodes} node${d.pendingNodes === 1 ? "" : "s"} still coming up.`}
          </InfoBanner>
        </div>
      )}

      {d.pools.length > 0 && (
        <div className="border-t border-slate-200">
          <table className="w-full border-collapse">
            <thead>
              <tr className="bg-slate-50">
                {["Node pool", "Shape", "Version", "Nodes", "State"].map((h, i) => (
                  <th
                    key={h}
                    scope="col"
                    className={cx(
                      "border-b border-slate-200 px-3 py-1.5 text-[11px] font-medium text-slate-600",
                      i === 3 ? "text-right" : "text-left",
                    )}
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {d.pools.map((p) => (
                <tr key={p.name}>
                  <td className="px-3 py-1.5 text-[13px] text-slate-800">{p.name}</td>
                  <td className="px-3 py-1.5 text-[12px] text-slate-600">
                    {p.shape ?? "—"}
                    {p.ocpus ? (
                      <span className="text-slate-400">
                        {" "}
                        · {p.ocpus} OCPU, {p.memory_gb} GB
                      </span>
                    ) : null}
                  </td>
                  <td className="px-3 py-1.5 font-mono text-[12px] text-slate-600">
                    {p.version ?? "—"}
                  </td>
                  <td className="px-3 py-1.5 text-right text-[13px] tabular-nums">
                    <span className="text-slate-800">{p.active}</span>
                    {p.desired !== p.active && (
                      <span className="text-slate-400"> / {p.desired} wanted</span>
                    )}
                  </td>
                  <td className="px-3 py-1.5 text-[12px]">
                    {p.failed > 0 ? (
                      <span className="text-st-down">{p.failed} failed</span>
                    ) : p.pending > 0 ? (
                      <span className="text-st-trouble">{p.pending} starting</span>
                    ) : (
                      <span className="text-st-up">ready</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

export function Kubernetes() {
  const list = useAsync(
    () => api.resources({ category: ["container", "kubernetes"], page_size: 100 }),
    [],
  );

  const clusters = list.data?.items ?? [];
  const totals = clusters.reduce(
    (acc, c) => {
      const d = clusterDetail(c);
      return {
        nodes: acc.nodes + d.activeNodes,
        pools: acc.pools + d.pools.length,
        upgradable: acc.upgradable + (d.upgrades.length > 0 ? 1 : 0),
        unhealthy: acc.unhealthy + d.failedNodes,
      };
    },
    { nodes: 0, pools: 0, upgradable: 0, unhealthy: 0 },
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Kubernetes Clusters"
        meta={clusters.length > 0 ? `${clusters.length} clusters` : undefined}
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {list.initialLoading ? (
          <Spinner label="Loading clusters" />
        ) : list.error ? (
          <ErrorState error={list.error} onRetry={list.reload} />
        ) : clusters.length === 0 ? (
          <Card>
            <EmptyState
              title="No clusters discovered"
              hint="Managed Kubernetes clusters appear here once a cloud account containing them has been collected."
            />
          </Card>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatTile label="Clusters" value={clusters.length} />
              <StatTile label="Active nodes" value={num(totals.nodes)} />
              <StatTile label="Node pools" value={totals.pools} />
              <StatTile
                label="Upgrade available"
                value={totals.upgradable}
                status={totals.upgradable > 0 ? "trouble" : undefined}
                hint={totals.upgradable > 0 ? "clusters behind the latest" : undefined}
              />
            </div>

            {totals.unhealthy > 0 && (
              <InfoBanner tone="warn">
                <span className="flex items-start gap-1.5">
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                  {totals.unhealthy} node{totals.unhealthy === 1 ? "" : "s"} across these clusters
                  is not healthy.
                </span>
              </InfoBanner>
            )}

            {clusters.map((c) => (
              <ClusterCard key={c.id} cluster={c} />
            ))}

            <p className="px-1 text-[11px] leading-relaxed text-slate-500">
              Node counts exclude nodes OCI has removed. It leaves them in the pool listing
              with state DELETED for a while, and counting those made a healthy cluster
              report failed nodes. What is measured here comes from the Container Engine
              service; the cluster's own metrics — unschedulable pods and control-plane
              request rate — are on each cluster's detail page. There is no in-cluster
              agent, so pods and workloads are not visible individually.
            </p>
          </>
        )}
      </div>
    </div>
  );
}

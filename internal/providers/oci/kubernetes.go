package oci

import (
	"context"
	"fmt"
	"sort"

	"github.com/oracle/oci-go-sdk/v65/containerengine"
)

// Kubernetes cluster enrichment.
//
// Monitoring publishes three metrics for an OKE cluster and none of them answers
// what somebody opening a Kubernetes section wants to know: what version is it, is
// an upgrade waiting, how many nodes are there, and are any of them unhealthy. All
// of that comes from the Container Engine service, so it is a separate call.
//
// Without it the section showed a row per cluster with a status dot, which is a
// monitor list rather than a Kubernetes view.

// NodeState is one worker node.
type NodeState struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	// AvailabilityDomain matters because a pool spread across domains losing one
	// domain looks the same as random node failures until you see where they are.
	AvailabilityDomain string `json:"availability_domain,omitempty"`
	PrivateIP          string `json:"private_ip,omitempty"`
	// Error is the reason OCI gives when a node cannot join or has failed.
	Error string `json:"error,omitempty"`
}

// NodePoolState is one node pool and the nodes inside it.
type NodePoolState struct {
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Version  string  `json:"version,omitempty"`
	Shape    string  `json:"shape,omitempty"`
	OCPUs    float32 `json:"ocpus,omitempty"`
	MemoryGB float32 `json:"memory_gb,omitempty"`
	// Desired is what the pool is configured for; Active is what is actually up.
	// The gap between them is the interesting number and is invisible from either
	// figure alone.
	Desired int `json:"desired"`
	Active  int `json:"active"`
	// Pending is creating or updating: present, not yet serving.
	Pending int `json:"pending"`
	// Failed is anything OCI reports as neither active nor in progress.
	Failed int         `json:"failed"`
	Nodes  []NodeState `json:"nodes,omitempty"`
}

// ClusterDetail is the Kubernetes view of a cluster.
type ClusterDetail struct {
	Version string `json:"version,omitempty"`
	// AvailableUpgrades is what OCI will let this cluster move to. An empty list
	// means it is current, which is worth stating rather than leaving blank.
	AvailableUpgrades []string `json:"available_upgrades,omitempty"`
	State             string   `json:"state,omitempty"`
	StateDetail       string   `json:"state_detail,omitempty"`
	Endpoint          string   `json:"endpoint,omitempty"`
	PrivateEndpoint   string   `json:"private_endpoint,omitempty"`

	NodePools []NodePoolState `json:"node_pools,omitempty"`
	// Totals across pools, so the page does not have to sum them and cannot
	// disagree with itself.
	// TotalNodes excludes removed nodes, so it is the size of the cluster as it
	// exists rather than as OCI's listing happens to report it.
	TotalNodes     int `json:"total_nodes"`
	ActiveNodes    int `json:"active_nodes"`
	PendingNodes   int `json:"pending_nodes"`
	UnhealthyNodes int `json:"unhealthy_nodes"`
}

// Node states, and what each one means for the counts.
//
// The first version of this treated anything not ACTIVE as unhealthy, which was
// wrong in the worst direction: OCI leaves removed nodes in the pool listing with
// state DELETED for a while, so a healthy production cluster reported "3 of 15
// nodes unhealthy". An alarming number that is false is worse than no number.
//
//	DELETED, DELETING  gone. Not part of the cluster, excluded from every count.
//	ACTIVE             serving.
//	CREATING, UPDATING present but not serving yet. Counted as not ready rather
//	                   than as failed, because a rolling upgrade passes through
//	                   here legitimately — but it is not counted as active either,
//	                   since a stalled upgrade would then look fine.
//	anything else      failed.
func nodeGone(state string) bool { return state == "DELETED" || state == "DELETING" }

func nodeActive(state string) bool { return state == "ACTIVE" }

func nodePending(state string) bool { return state == "CREATING" || state == "UPDATING" }

// ClusterDetails fetches version, upgrades and node pool health for one cluster.
func (c *Client) ClusterDetails(ctx context.Context, clusterOCID, compartmentOCID string) (ClusterDetail, error) {
	var out ClusterDetail
	if c.ce == nil {
		return out, fmt.Errorf("oci: container engine client not configured")
	}
	if err := c.gate.wait(ctx); err != nil {
		return out, err
	}

	cl, err := c.ce.GetCluster(ctx, containerengine.GetClusterRequest{ClusterId: &clusterOCID})
	if err != nil {
		return out, fmt.Errorf("oci: get cluster: %w", err)
	}
	if cl.KubernetesVersion != nil {
		out.Version = *cl.KubernetesVersion
	}
	out.AvailableUpgrades = cl.AvailableKubernetesUpgrades
	out.State = string(cl.LifecycleState)
	if cl.LifecycleDetails != nil {
		out.StateDetail = *cl.LifecycleDetails
	}
	if cl.Endpoints != nil {
		if cl.Endpoints.Kubernetes != nil {
			out.Endpoint = *cl.Endpoints.Kubernetes
		}
		if cl.Endpoints.PrivateEndpoint != nil {
			out.PrivateEndpoint = *cl.Endpoints.PrivateEndpoint
		}
	}

	// Node pools live in the compartment, not under the cluster, so the list has to
	// be filtered by cluster id.
	comp := compartmentOCID
	if comp == "" {
		comp = c.cfg.TenancyOCID
	}
	if err := c.gate.wait(ctx); err != nil {
		return out, nil
	}
	pools, err := c.ce.ListNodePools(ctx, containerengine.ListNodePoolsRequest{
		CompartmentId: &comp,
		ClusterId:     &clusterOCID,
	})
	if err != nil {
		// Version without pools is still worth having: it answers the upgrade
		// question on its own.
		return out, nil
	}

	for _, ps := range pools.Items {
		p := NodePoolState{State: string(ps.LifecycleState)}
		if ps.Name != nil {
			p.Name = *ps.Name
		}
		if ps.KubernetesVersion != nil {
			p.Version = *ps.KubernetesVersion
		}
		if ps.NodeShape != nil {
			p.Shape = *ps.NodeShape
		}
		if ps.NodeShapeConfig != nil {
			if ps.NodeShapeConfig.Ocpus != nil {
				p.OCPUs = *ps.NodeShapeConfig.Ocpus
			}
			if ps.NodeShapeConfig.MemoryInGBs != nil {
				p.MemoryGB = *ps.NodeShapeConfig.MemoryInGBs
			}
		}
		if ps.NodeConfigDetails != nil && ps.NodeConfigDetails.Size != nil {
			p.Desired = *ps.NodeConfigDetails.Size
		}

		// The summary list does not carry nodes, so the pool has to be fetched.
		if ps.Id != nil {
			if err := c.gate.wait(ctx); err == nil {
				if np, err := c.ce.GetNodePool(ctx,
					containerengine.GetNodePoolRequest{NodePoolId: ps.Id}); err == nil {
					for _, n := range np.Nodes {
						ns := NodeState{State: string(n.LifecycleState)}
						if nodeGone(ns.State) {
							// Removed nodes are not part of the cluster. Skipped
							// entirely rather than counted, so a pool that has
							// scaled down does not look damaged.
							continue
						}
						if n.Name != nil {
							ns.Name = *n.Name
						}
						if n.KubernetesVersion != nil {
							ns.Version = *n.KubernetesVersion
						}
						if n.AvailabilityDomain != nil {
							ns.AvailabilityDomain = *n.AvailabilityDomain
						}
						if n.PrivateIp != nil {
							ns.PrivateIP = *n.PrivateIp
						}
						if n.NodeError != nil && n.NodeError.Message != nil {
							ns.Error = *n.NodeError.Message
						}
						switch {
						case nodeActive(ns.State):
							p.Active++
						case nodePending(ns.State):
							p.Pending++
						default:
							p.Failed++
							out.UnhealthyNodes++
						}
						p.Nodes = append(p.Nodes, ns)
					}
					if p.Desired == 0 {
						p.Desired = len(np.Nodes)
					}
				}
			}
		}
		out.TotalNodes += len(p.Nodes)
		out.ActiveNodes += p.Active
		out.PendingNodes += p.Pending
		sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].Name < p.Nodes[j].Name })
		out.NodePools = append(out.NodePools, p)
	}
	sort.Slice(out.NodePools, func(i, j int) bool { return out.NodePools[i].Name < out.NodePools[j].Name })
	return out, nil
}

package oci

import (
	"context"
	"fmt"
	"sort"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

// Load balancer enrichment.
//
// Two things a metric alone cannot tell you, and both are the first questions asked
// when a load balancer alarm fires:
//
//	Which load balancer is this? Resource Search returns the display name, and in
//	many tenancies that is a UUID nobody recognises. The address is what people
//	actually know it by.
//
//	Which backend is down? "Unhealthy Backends 3" says a number. The number is not
//	actionable; the three hostnames are.
//
// Both come from the Load Balancer service rather than from Monitoring, so this is a
// separate call made only for load balancers.

// BackendHealth is one backend's state within a set.
type BackendHealth struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// BackendSetHealth is one backend set and the backends failing inside it.
type BackendSetHealth struct {
	Name string `json:"name"`
	// Status is OCI's overall verdict: OK, WARNING, CRITICAL or UNKNOWN.
	Status        string          `json:"status"`
	TotalBackends int             `json:"total_backends"`
	Critical      []BackendHealth `json:"critical,omitempty"`
	Warning       []BackendHealth `json:"warning,omitempty"`
	Unknown       []BackendHealth `json:"unknown,omitempty"`
}

// LoadBalancerDetail is what a load balancer alarm needs beyond a number.
type LoadBalancerDetail struct {
	// Addresses are the IPs the load balancer answers on, public first. This is how
	// people identify a load balancer whose display name is a UUID.
	Addresses []string `json:"addresses,omitempty"`
	Shape     string   `json:"shape,omitempty"`
	// OverallHealth is OCI's verdict for the whole load balancer.
	OverallHealth string             `json:"overall_health,omitempty"`
	BackendSets   []BackendSetHealth `json:"backend_sets,omitempty"`
	// Listeners maps a listener name to the port and protocol it serves, so the
	// email can say which entry point is affected.
	Listeners []string `json:"listeners,omitempty"`
}

// UnhealthyBackends flattens the failing backends into names, worst first.
//
// Returned separately because the email needs a short list, not a tree.
func (d LoadBalancerDetail) UnhealthyBackends() []string {
	var out []string
	for _, bs := range d.BackendSets {
		for _, b := range bs.Critical {
			out = append(out, fmt.Sprintf("%s in %s", b.Name, bs.Name))
		}
	}
	for _, bs := range d.BackendSets {
		for _, b := range bs.Warning {
			out = append(out, fmt.Sprintf("%s in %s (warning)", b.Name, bs.Name))
		}
	}
	return out
}

// LoadBalancerDetails fetches identity and backend health for one load balancer.
//
// Failures are returned rather than swallowed, but the caller is expected to carry
// on without the detail: an alert with a number and no backend list is worse than
// one with both and far better than none.
func (c *Client) LoadBalancerDetails(ctx context.Context, ocid string) (LoadBalancerDetail, error) {
	var out LoadBalancerDetail
	if c.lb == nil {
		return out, fmt.Errorf("oci: load balancer client not configured")
	}
	if err := c.gate.wait(ctx); err != nil {
		return out, err
	}

	lbResp, err := c.lb.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &ocid,
	})
	if err != nil {
		return out, fmt.Errorf("oci: get load balancer: %w", err)
	}
	for _, ip := range lbResp.LoadBalancer.IpAddresses {
		if ip.IpAddress == nil {
			continue
		}
		addr := *ip.IpAddress
		// Public first: that is the address someone recognises and the one a
		// customer is complaining about.
		if ip.IsPublic != nil && *ip.IsPublic {
			out.Addresses = append([]string{addr}, out.Addresses...)
		} else {
			out.Addresses = append(out.Addresses, addr)
		}
	}
	if lbResp.LoadBalancer.ShapeName != nil {
		out.Shape = *lbResp.LoadBalancer.ShapeName
	}
	for name, l := range lbResp.LoadBalancer.Listeners {
		port, proto := 0, ""
		if l.Port != nil {
			port = *l.Port
		}
		if l.Protocol != nil {
			proto = *l.Protocol
		}
		out.Listeners = append(out.Listeners, fmt.Sprintf("%s %s/%d", name, proto, port))
	}
	sort.Strings(out.Listeners)

	if err := c.gate.wait(ctx); err != nil {
		return out, err
	}
	health, err := c.lb.GetLoadBalancerHealth(ctx, loadbalancer.GetLoadBalancerHealthRequest{
		LoadBalancerId: &ocid,
	})
	if err != nil {
		// Identity without health is still worth having: the address alone makes the
		// alert identifiable.
		return out, nil
	}
	out.OverallHealth = string(health.Status)

	// Only sets OCI already flags as not OK are inspected. Asking for every set's
	// backend list on a healthy load balancer is a call per set for no information.
	interesting := map[string]string{}
	for _, n := range health.CriticalStateBackendSetNames {
		interesting[n] = "CRITICAL"
	}
	for _, n := range health.WarningStateBackendSetNames {
		interesting[n] = "WARNING"
	}
	for _, n := range health.UnknownStateBackendSetNames {
		interesting[n] = "UNKNOWN"
	}

	names := make([]string, 0, len(interesting))
	for n := range interesting {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		bs := BackendSetHealth{Name: name, Status: interesting[name]}
		if err := c.gate.wait(ctx); err != nil {
			break
		}
		bh, err := c.lb.GetBackendSetHealth(ctx, loadbalancer.GetBackendSetHealthRequest{
			LoadBalancerId: &ocid,
			BackendSetName: common.String(name),
		})
		if err == nil {
			if bh.TotalBackendCount != nil {
				bs.TotalBackends = *bh.TotalBackendCount
			}
			for _, b := range bh.CriticalStateBackendNames {
				bs.Critical = append(bs.Critical, BackendHealth{Name: b, Status: "CRITICAL"})
			}
			for _, b := range bh.WarningStateBackendNames {
				bs.Warning = append(bs.Warning, BackendHealth{Name: b, Status: "WARNING"})
			}
			for _, b := range bh.UnknownStateBackendNames {
				bs.Unknown = append(bs.Unknown, BackendHealth{Name: b, Status: "UNKNOWN"})
			}
		}
		out.BackendSets = append(out.BackendSets, bs)
	}
	return out, nil
}

// CompartmentNames maps compartment OCIDs to their names, including the tenancy
// root, which the compartment list does not return.
//
// Stored against every resource because an OCID identifies a compartment to the API
// and to nobody else. "Which compartment is this in" is the first question after
// "what is broken", and answering it should not need a console tab.
func (c *Client) CompartmentNames(ctx context.Context) (map[string]string, error) {
	out := map[string]string{c.cfg.TenancyOCID: "root"}
	comps, err := c.Compartments(ctx)
	if err != nil {
		return out, err
	}
	for _, cm := range comps {
		if cm.Id == nil || cm.Name == nil {
			continue
		}
		out[*cm.Id] = *cm.Name
	}
	return out, nil
}

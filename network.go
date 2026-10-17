package main

// The network card: the IPs that matter for firewall and networking decisions.
//
//   - Ingress: the gateways' addresses, and which node announces each one
//     (Cilium Layer 2 announcements keep that in a lease).
//   - Egress: the nodes' internal addresses, which is where pod traffic leaves from.
//
// Everything is read-only; see charts/view/templates/rbac.yaml for exactly what may be read.

import (
	"context"
	"errors"
	"log"
	"net/netip"
	"sort"
	"time"
)

const (
	clusterEvery = time.Minute // how often addresses are re-read
	newNodeFor   = 7 * 24 * time.Hour
)

type NetworkStatus struct {
	Error   string      `json:"error,omitempty"` // the cluster couldn't be read
	Ingress []IngressIP `json:"ingress"`
	Egress  []EgressIP  `json:"egress"`
}

type IngressIP struct {
	IP      string `json:"ip"`
	Node    string `json:"node,omitempty"` // the node currently announcing it
	Gateway string `json:"gateway"`
}

type EgressIP struct {
	IP   string `json:"ip"`
	Node string `json:"node"`
	New  bool   `json:"new,omitempty"` // joined in the last week
}

type networkWatcher struct {
	cfg     *config
	kube    *kube
	kubeErr error
	lastErr string
}

func newNetworkWatcher(cfg *config) *networkWatcher {
	k, err := newKube()
	return &networkWatcher{cfg: cfg, kube: k, kubeErr: err}
}

func (n *networkWatcher) run(ctx context.Context) {
	t := time.NewTicker(clusterEvery)
	defer t.Stop()
	for {
		n.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (n *networkWatcher) fail(msg string) {
	if msg != n.lastErr {
		log.Printf("network card: %s", msg)
		n.lastErr = msg
	}
	n.cfg.setNetwork(&NetworkStatus{Error: msg, Ingress: []IngressIP{}, Egress: []EgressIP{}})
}

func (n *networkWatcher) tick(parent context.Context) {
	if !n.cfg.networkOn() {
		n.cfg.setNetwork(nil)
		return
	}
	if n.kube == nil {
		n.fail(n.kubeErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	var gws gatewayList
	var nodes nodeList
	if err := n.kube.get(ctx, "/apis/gateway.networking.k8s.io/v1/gateways", &gws); err != nil {
		if errors.Is(err, errNotFound) {
			err = errors.New("the Gateway API isn't installed in this cluster")
		}
		n.fail(err.Error())
		return
	}
	if err := n.kube.get(ctx, "/api/v1/nodes", &nodes); err != nil {
		n.fail(err.Error())
		return
	}
	if n.lastErr != "" {
		log.Printf("network card: reading the cluster works again")
		n.lastErr = ""
	}

	// Which node announces each load-balancer address. Optional: without these
	// permissions (or without Cilium) the card simply leaves the node out.
	announcer := map[string]string{}
	var svcs serviceList
	var leases leaseList
	if n.kube.get(ctx, "/api/v1/services", &svcs) == nil && n.kube.get(ctx, "/apis/coordination.k8s.io/v1/namespaces/kube-system/leases", &leases) == nil {
		holder := map[string]string{}
		for _, l := range leases.Items {
			if l.Spec.HolderIdentity != nil {
				holder[l.Metadata.Name] = *l.Spec.HolderIdentity
			}
		}
		for _, s := range svcs.Items {
			if s.Spec.Type != "LoadBalancer" {
				continue
			}
			if h, ok := holder["cilium-l2announce-"+s.Metadata.Namespace+"-"+s.Metadata.Name]; ok {
				for _, in := range s.Status.LoadBalancer.Ingress {
					announcer[in.IP] = h
				}
			}
		}
	}

	ingress := []IngressIP{}
	for _, g := range gws.Items {
		for _, a := range g.Status.Addresses {
			ingress = append(ingress, IngressIP{IP: a.Value, Node: announcer[a.Value], Gateway: g.Metadata.Namespace + "/" + g.Metadata.Name})
		}
	}
	sort.Slice(ingress, func(i, j int) bool { return ipLess(ingress[i].IP, ingress[j].IP) })

	// Pod traffic leaves with the address of its node.
	egress := []EgressIP{}
	for _, nd := range nodes.Items {
		for _, a := range nd.Status.Addresses {
			if a.Type == "InternalIP" {
				egress = append(egress, EgressIP{IP: a.Address, Node: nd.Metadata.Name, New: time.Since(nd.Metadata.CreationTimestamp) < newNodeFor})
			}
		}
	}
	sort.Slice(egress, func(i, j int) bool { return ipLess(egress[i].IP, egress[j].IP) })

	n.cfg.setNetwork(&NetworkStatus{Ingress: ingress, Egress: egress})
}

func ipLess(a, b string) bool {
	x, errA := netip.ParseAddr(a)
	y, errB := netip.ParseAddr(b)
	if errA != nil || errB != nil {
		return a < b
	}
	return x.Less(y)
}

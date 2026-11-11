package main

// The platform footer: which versions of Kubernetes and the platform's own
// components (CNI, Gateway API, GitOps, cert-manager, …) run in this cluster,
// each linking to its GitHub release.
//
// Nothing needs labelling or listing: components are recognised by their
// container images, however they were installed (Flux, k3s, plain manifests).
// Kubernetes comes from the version API, Flux from its controllers' version
// label and the Gateway API from its CRD. Workloads are not shown.

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

const toolingEvery = 2 * time.Minute

// A component view knows how to recognise.
type component struct {
	ID      string   // for tooling.hide
	Name    string   // what the badge says
	Repo    string   // GitHub owner/repo, for the release link
	Images  []string // image paths it runs as (matched on whole path segments at the end)
	Label   bool     // take the version from the app.kubernetes.io/version label, not the image tag
	BareTag bool     // the repo's release tags have no "v" (kured: 1.23.0)
}

// The order here is the order on the page: Kubernetes, networking, GitOps, the rest.
var catalog = []component{
	{ID: "kubernetes", Name: "Kubernetes"}, // from the version API, see kubernetesBadge
	{ID: "cilium", Name: "Cilium", Repo: "cilium/cilium", Images: []string{"cilium/cilium", "rancher/mirrored-cilium-cilium"}},
	{ID: "gateway-api", Name: "Gateway API", Repo: "kubernetes-sigs/gateway-api"}, // from its CRD
	{ID: "kgateway", Name: "kgateway", Repo: "kgateway-dev/kgateway", Images: []string{"kgateway-dev/kgateway"}},
	{ID: "coredns", Name: "CoreDNS", Repo: "coredns/coredns", Images: []string{"coredns/coredns", "rancher/mirrored-coredns-coredns"}},
	{ID: "flux", Name: "Flux", Repo: "fluxcd/flux2", Label: true, Images: []string{
		"fluxcd/source-controller", "fluxcd/kustomize-controller", "fluxcd/helm-controller", "fluxcd/notification-controller"}},
	{ID: "flux-operator", Name: "flux-operator", Repo: "controlplaneio-fluxcd/flux-operator", Images: []string{"controlplaneio-fluxcd/flux-operator"}},
	{ID: "cert-manager", Name: "cert-manager", Repo: "cert-manager/cert-manager", Images: []string{"jetstack/cert-manager-controller"}},
	{ID: "cloudnative-pg", Name: "CloudNativePG", Repo: "cloudnative-pg/cloudnative-pg", Images: []string{"cloudnative-pg/cloudnative-pg"}},
	{ID: "csi-driver-nfs", Name: "CSI NFS", Repo: "kubernetes-csi/csi-driver-nfs", Images: []string{"sig-storage/nfsplugin", "rancher/mirrored-sig-storage-nfsplugin"}},
	{ID: "metrics-server", Name: "metrics-server", Repo: "kubernetes-sigs/metrics-server", Images: []string{"metrics-server/metrics-server", "rancher/mirrored-metrics-server"}},
	{ID: "kured", Name: "kured", Repo: "kubereboot/kured", Images: []string{"kubereboot/kured"}, BareTag: true},
}

func componentIDs() []string {
	ids := make([]string, len(catalog))
	for i, c := range catalog {
		ids[i] = c.ID
	}
	return ids
}

type ToolingStatus struct {
	Error string        `json:"error,omitempty"` // the cluster couldn't be read
	Items []ToolingItem `json:"items"`
}

type ToolingItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`        // e.g. v1.20.1; two during a rollout: "v1.20.1 / v1.20.0"
	Link    string `json:"link,omitempty"` // the GitHub release
}

type workloadList struct {
	Items []struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	} `json:"items"`
}

// running is one container image found in the cluster.
type running struct {
	image string // without tag and digest, e.g. quay.io/cilium/cilium
	tag   string // e.g. v1.20.1; "" when pinned by digest only
	label string // the workload's app.kubernetes.io/version
}

type toolingWatcher struct {
	cfg     *config
	kube    *kube
	kubeErr error
	lastErr string
}

func newToolingWatcher(cfg *config) *toolingWatcher {
	k, err := newKube()
	return &toolingWatcher{cfg: cfg, kube: k, kubeErr: err}
}

func (w *toolingWatcher) run(ctx context.Context) {
	// Checked often for the config switch, but the cluster is only read every toolingEvery.
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var last time.Time
	var lastCfg *ToolingConfig
	for {
		tc := w.cfg.toolingConfig()
		if tc == nil {
			w.cfg.setTooling(nil)
			last, lastCfg = time.Time{}, nil
		} else if tc != lastCfg || time.Since(last) >= toolingEvery {
			w.tick(ctx, tc)
			last, lastCfg = time.Now(), tc
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *toolingWatcher) fail(msg string) {
	if msg != w.lastErr {
		log.Printf("platform footer: %s", msg)
		w.lastErr = msg
	}
	w.cfg.setTooling(&ToolingStatus{Error: msg, Items: []ToolingItem{}})
}

func (w *toolingWatcher) tick(parent context.Context, tc *ToolingConfig) {
	if w.kube == nil {
		w.fail(w.kubeErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	var found []running
	for _, kind := range []string{"deployments", "daemonsets", "statefulsets"} {
		var l workloadList
		if err := w.kube.get(ctx, "/apis/apps/v1/"+kind, &l); err != nil {
			w.fail(err.Error())
			return
		}
		for _, it := range l.Items {
			for _, c := range it.Spec.Template.Spec.Containers {
				img, tag := splitImage(c.Image)
				found = append(found, running{image: img, tag: tag, label: it.Metadata.Labels["app.kubernetes.io/version"]})
			}
		}
	}
	if w.lastErr != "" {
		log.Printf("platform footer: reading the cluster works again")
		w.lastErr = ""
	}

	// Both of these are optional: without them the badge is simply left out.
	var ver struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := w.kube.get(ctx, "/version", &ver); err != nil {
		ver.GitVersion = ""
	}
	var crd struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	gatewayAPI := ""
	if err := w.kube.get(ctx, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/gateways.gateway.networking.k8s.io", &crd); err == nil {
		gatewayAPI = crd.Metadata.Annotations["gateway.networking.k8s.io/bundle-version"]
	} else if !errors.Is(err, errNotFound) {
		log.Printf("platform footer: Gateway API version: %v", err)
	}

	w.cfg.setTooling(&ToolingStatus{Items: toolingItems(tc, ver.GitVersion, gatewayAPI, found)})
}

// toolingItems turns what was found into badges, in catalog order, extras last.
func toolingItems(tc *ToolingConfig, gitVersion, gatewayAPI string, found []running) []ToolingItem {
	items := []ToolingItem{}
	hidden := map[string]bool{}
	for _, h := range tc.Hide {
		hidden[h] = true
	}
	for _, c := range catalog {
		if hidden[c.ID] {
			continue
		}
		switch c.ID {
		case "kubernetes":
			if it, ok := kubernetesBadge(gitVersion); ok {
				items = append(items, it)
			}
		case "gateway-api":
			if v := cleanVersion(gatewayAPI); v != "" {
				items = append(items, ToolingItem{Name: c.Name, Version: v, Link: releaseLink(c.Repo, v)})
			}
		default:
			tagOf := func(v, _ string) string { return v }
			if c.BareTag {
				tagOf = func(v, _ string) string { return strings.TrimPrefix(v, "v") }
			}
			if it, ok := badge(c.Name, c.Repo, c.Images, c.Label, tagOf, found); ok {
				items = append(items, it)
			}
		}
	}
	for _, x := range tc.Extra {
		// for components view doesn't know, the release tag is taken to be the image tag as it is
		if it, ok := badge(x.Name, x.Repo, []string{x.Image}, false, func(_, raw string) string { return raw }, found); ok {
			items = append(items, it)
		}
	}
	return items
}

// badge finds a component's versions among the running images. Several
// versions (in the middle of a rollout, say) are all shown, highest first.
// tagOf gives the release tag for the link, from the shown and the raw version.
func badge(name, repo string, images []string, useLabel bool, tagOf func(shown, raw string) string, found []running) (ToolingItem, bool) {
	raw := map[string]string{}
	var versions []string
	for _, r := range found {
		if !imageMatches(r.image, images) {
			continue
		}
		v := r.tag
		if useLabel {
			v = r.label
		}
		if c := cleanVersion(v); c != "" && raw[c] == "" {
			raw[c] = strings.TrimSpace(v)
			versions = append(versions, c)
		}
	}
	if len(versions) == 0 {
		return ToolingItem{}, false
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	it := ToolingItem{Name: name, Version: strings.Join(versions, " / ")}
	if repo != "" {
		it.Link = releaseLink(repo, tagOf(versions[0], raw[versions[0]]))
	}
	return it, true
}

func releaseLink(repo, tag string) string {
	return "https://github.com/" + repo + "/releases/tag/" + tag
}

// kubernetesBadge shows the version short, named after the distribution when
// the version says which one it is: v1.36.3+k3s1 is "k3s v1.36.3".
func kubernetesBadge(gitVersion string) (ToolingItem, bool) {
	gv := strings.TrimSpace(gitVersion)
	base := gv
	if i := strings.IndexAny(gv, "+-"); i > 0 {
		base = gv[:i]
	}
	base = cleanVersion(base)
	if base == "" {
		return ToolingItem{}, false
	}
	switch {
	case strings.Contains(gv, "+k3s"):
		return ToolingItem{Name: "k3s", Version: base, Link: releaseLink("k3s-io/k3s", gv)}, true
	case strings.Contains(gv, "+rke2"):
		return ToolingItem{Name: "RKE2", Version: base, Link: releaseLink("rancher/rke2", gv)}, true
	case strings.Contains(gv, "-eks-"):
		return ToolingItem{Name: "EKS", Version: base, Link: releaseLink("kubernetes/kubernetes", base)}, true
	case strings.Contains(gv, "-gke."):
		return ToolingItem{Name: "GKE", Version: base, Link: releaseLink("kubernetes/kubernetes", base)}, true
	}
	return ToolingItem{Name: "Kubernetes", Version: base, Link: releaseLink("kubernetes/kubernetes", base)}, true
}

// splitImage turns "quay.io/cilium/cilium:v1.20.1@sha256:…" into ("quay.io/cilium/cilium", "v1.20.1").
func splitImage(ref string) (image, tag string) {
	ref, _, _ = strings.Cut(strings.TrimSpace(ref), "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// imageMatches: "quay.io/cilium/cilium" matches "cilium/cilium", but
// "quay.io/cilium/cilium-envoy" and "quay.io/notcilium/cilium" don't.
func imageMatches(image string, patterns []string) bool {
	image = strings.ToLower(image)
	for _, p := range patterns {
		p = strings.ToLower(strings.Trim(p, "/"))
		if p != "" && (image == p || strings.HasSuffix(image, "/"+p)) {
			return true
		}
	}
	return false
}

// cleanVersion accepts versions like 1.30.0, v1.20.1 or v2.4.3-patch1 and gives them a "v";
// anything else (latest, main, a digest) isn't a version to show.
func cleanVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || v[0] < '0' || v[0] > '9' || strings.ContainsAny(v, " /") {
		return ""
	}
	return "v" + v
}

// versionLess compares the numbers in two versions (v1.9.0 < v1.10.0).
func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	if len(pa) != len(pb) {
		return len(pa) < len(pb)
	}
	return a < b
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

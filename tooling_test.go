package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitImage(t *testing.T) {
	cases := map[string][2]string{
		"quay.io/cilium/cilium:v1.20.1@sha256:ae9e":    {"quay.io/cilium/cilium", "v1.20.1"},
		"rancher/mirrored-coredns-coredns:1.14.6":      {"rancher/mirrored-coredns-coredns", "1.14.6"},
		"localhost:5000/kured":                         {"localhost:5000/kured", ""},
		"registry.example.com:5000/team/app:1.2":       {"registry.example.com:5000/team/app", "1.2"},
		"ghcr.io/fluxcd/source-controller@sha256:abcd": {"ghcr.io/fluxcd/source-controller", ""},
		"haproxy": {"haproxy", ""},
	}
	for in, want := range cases {
		img, tag := splitImage(in)
		if img != want[0] || tag != want[1] {
			t.Errorf("%s: got %q %q, want %q %q", in, img, tag, want[0], want[1])
		}
	}
}

func TestImageMatches(t *testing.T) {
	yes := [][2]string{
		{"quay.io/cilium/cilium", "cilium/cilium"},
		{"cilium/cilium", "cilium/cilium"},
		{"registry.k8s.io/sig-storage/nfsplugin", "sig-storage/nfsplugin"},
		{"Quay.io/Jetstack/cert-manager-controller", "jetstack/cert-manager-controller"},
	}
	no := [][2]string{
		{"quay.io/cilium/cilium-envoy", "cilium/cilium"},
		{"quay.io/notcilium/cilium", "cilium/cilium"},
		{"quay.io/cilium/operator-generic", "cilium/cilium"},
		{"registry.k8s.io/sig-storage/csi-provisioner", "sig-storage/nfsplugin"},
	}
	for _, c := range yes {
		if !imageMatches(c[0], []string{c[1]}) {
			t.Errorf("%s should match %s", c[0], c[1])
		}
	}
	for _, c := range no {
		if imageMatches(c[0], []string{c[1]}) {
			t.Errorf("%s shouldn't match %s", c[0], c[1])
		}
	}
}

func TestKubernetesBadge(t *testing.T) {
	cases := map[string]ToolingItem{
		"v1.36.3+k3s1":        {Name: "k3s", Version: "v1.36.3", Link: "https://github.com/k3s-io/k3s/releases/tag/v1.36.3+k3s1"},
		"v1.36.3+rke2r1":      {Name: "RKE2", Version: "v1.36.3", Link: "https://github.com/rancher/rke2/releases/tag/v1.36.3+rke2r1"},
		"v1.34.2-eks-113cf36": {Name: "EKS", Version: "v1.34.2", Link: "https://github.com/kubernetes/kubernetes/releases/tag/v1.34.2"},
		"v1.34.1-gke.1829001": {Name: "GKE", Version: "v1.34.1", Link: "https://github.com/kubernetes/kubernetes/releases/tag/v1.34.1"},
		"v1.36.0":             {Name: "Kubernetes", Version: "v1.36.0", Link: "https://github.com/kubernetes/kubernetes/releases/tag/v1.36.0"},
	}
	for in, want := range cases {
		got, ok := kubernetesBadge(in)
		if !ok || got != want {
			t.Errorf("%s: got %+v", in, got)
		}
	}
	if _, ok := kubernetesBadge(""); ok {
		t.Error("an empty version shouldn't give a badge")
	}
}

// What staging runs (from the discovery of 2026-11-11), plus some noise.
var stagingImages = []string{
	"internetsystemsconsortium/bind9:9.20",
	"ghcr.io/controlplaneio-fluxcd/flux-operator:v0.62.1",
	"ghcr.io/fluxcd/helm-controller:v1.7.1@sha256:5452",
	"ghcr.io/fluxcd/kustomize-controller:v1.10.1@sha256:37db",
	"haproxy:lts-alpine3.24",
	"cr.kgateway.dev/kgateway-dev/kgateway:v2.4.3",
	"quay.io/cilium/operator-generic:v1.20.1@sha256:6c38",
	"rancher/mirrored-coredns-coredns:1.14.6",
	"registry.k8s.io/sig-storage/csi-provisioner:v6.3.0",
	"registry.k8s.io/sig-storage/nfsplugin:v4.13.5",
	"quay.io/cilium/hubble-relay:v1.20.1@sha256:59be",
	"rancher/mirrored-metrics-server:v0.9.0",
	"quay.io/cilium/cilium:v1.20.1@sha256:ae9e",
	"quay.io/cilium/cilium-envoy:v1.37.5-1786810558-766ccfb@sha256:75b8",
	"quay.io/jetstack/cert-manager-controller:v1.21.1",
	"quay.io/jetstack/cert-manager-webhook:v1.21.1",
	"ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0",
	"ghcr.io/kubereboot/kured:1.23.0",
	"goauthentik/server:2026.8.5",
}

func stagingFound() []running {
	var found []running
	for _, ref := range stagingImages {
		img, tag := splitImage(ref)
		r := running{image: img, tag: tag}
		if strings.Contains(img, "fluxcd/") && !strings.Contains(img, "flux-operator") {
			r.label = "v2.10.0"
		}
		found = append(found, r)
	}
	return found
}

func TestToolingItemsStaging(t *testing.T) {
	got := toolingItems(&ToolingConfig{}, "v1.36.3+k3s1", "v1.6.1", stagingFound())
	want := []string{
		"k3s v1.36.3 https://github.com/k3s-io/k3s/releases/tag/v1.36.3+k3s1",
		"Cilium v1.20.1 https://github.com/cilium/cilium/releases/tag/v1.20.1",
		"Gateway API v1.6.1 https://github.com/kubernetes-sigs/gateway-api/releases/tag/v1.6.1",
		"kgateway v2.4.3 https://github.com/kgateway-dev/kgateway/releases/tag/v2.4.3",
		"CoreDNS v1.14.6 https://github.com/coredns/coredns/releases/tag/v1.14.6",
		"Flux v2.10.0 https://github.com/fluxcd/flux2/releases/tag/v2.10.0",
		"flux-operator v0.62.1 https://github.com/controlplaneio-fluxcd/flux-operator/releases/tag/v0.62.1",
		"cert-manager v1.21.1 https://github.com/cert-manager/cert-manager/releases/tag/v1.21.1",
		"CloudNativePG v1.30.0 https://github.com/cloudnative-pg/cloudnative-pg/releases/tag/v1.30.0",
		"CSI NFS v4.13.5 https://github.com/kubernetes-csi/csi-driver-nfs/releases/tag/v4.13.5",
		"metrics-server v0.9.0 https://github.com/kubernetes-sigs/metrics-server/releases/tag/v0.9.0",
		"kured v1.23.0 https://github.com/kubereboot/kured/releases/tag/1.23.0",
	}
	var lines []string
	for _, it := range got {
		lines = append(lines, it.Name+" "+it.Version+" "+it.Link)
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestToolingHideExtraAndRollout(t *testing.T) {
	found := append(stagingFound(), running{image: "quay.io/cilium/cilium", tag: "v1.19.4"}, running{image: "quay.io/cilium/cilium", tag: "latest"})
	tc := &ToolingConfig{
		Hide:  []string{"kured", "gateway-api", "kubernetes"},
		Extra: []ExtraTooling{{Name: "BIND", Image: "internetsystemsconsortium/bind9", Repo: "isc-projects/bind9"}, {Name: "Missing", Image: "nothing/here"}},
	}
	got := toolingItems(tc, "v1.36.3+k3s1", "v1.6.1", found)
	names := []string{}
	for _, it := range got {
		names = append(names, it.Name)
	}
	if strings.Join(names, ",") != "Cilium,kgateway,CoreDNS,Flux,flux-operator,cert-manager,CloudNativePG,CSI NFS,metrics-server,BIND" {
		t.Errorf("names: %v", names)
	}
	if got[0].Version != "v1.20.1 / v1.19.4" || !strings.HasSuffix(got[0].Link, "/v1.20.1") {
		t.Errorf("rollout: %+v", got[0])
	}
	if last := got[len(got)-1]; last.Version != "v9.20" || last.Link != "https://github.com/isc-projects/bind9/releases/tag/9.20" {
		t.Errorf("extra: %+v", last)
	}
}

func TestToolingConfig(t *testing.T) {
	b, err := parseConfig([]byte("platform:\n  tooling:\n    hide: [Kured]\n    extra:\n      - name: BIND\n        image: internetsystemsconsortium/bind9\n"))
	if err != nil {
		t.Fatal(err)
	}
	tc := b.PlatformCfg.Tooling
	if tc == nil || !reflect.DeepEqual(tc.Hide, []string{"kured"}) || len(tc.Extra) != 1 || tc.Extra[0].Repo != "" {
		t.Errorf("got %+v", tc)
	}
	if b, _ := parseConfig([]byte("platform:\n  tooling: {}\n")); b.PlatformCfg.Tooling == nil {
		t.Error("tooling: {} should switch the footer on")
	}
	if b, _ := parseConfig([]byte("platform:\n  network: false\n")); b.PlatformCfg.Tooling != nil {
		t.Error("no tooling block should leave the footer off")
	}

	bad := map[string]string{
		"hide: [calico]":                                  "isn't a component view knows",
		"extra:\n      - image: a/b":                      "needs a name",
		"extra:\n      - name: X":                         "needs image",
		"extra:\n      - name: X\n        image: a/b:1.0": "leave the tag off",
		"extra:\n      - name: X\n        image: a/b\n        repo: https://github.com/a/b": "should be a GitHub owner/name",
		"extra:\n      - name: X\n        imgae: a/b":                                       "is not an extra tooling setting",
		"hdie: [kured]": "is not a tooling setting",
	}
	for y, want := range bad {
		_, err := parseConfig([]byte("platform:\n  tooling:\n    " + y + "\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want one containing %q", y, err, want)
		}
	}
}

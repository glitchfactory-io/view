package main

import (
	"encoding/json"
	"testing"
)

func TestTileKey(t *testing.T) {
	same := [][2]string{
		{"https://harbor.example.com", "https://harbor.example.com/"},
		{"http://Harbor.Example.com/", "https://harbor.example.com"},
		{"https://harbor.example.com:443/", "https://harbor.example.com"},
		{"https://example.com/grafana/", "https://example.com/grafana"},
	}
	for _, p := range same {
		if tileKey(p[0]) != tileKey(p[1]) {
			t.Errorf("%q and %q should count as the same link", p[0], p[1])
		}
	}
	different := [][2]string{
		{"https://example.com/grafana", "https://example.com/prometheus"},
		{"https://example.com:8443/", "https://example.com/"},
		{"https://example.com/?a=1", "https://example.com/?a=2"},
	}
	for _, p := range different {
		if tileKey(p[0]) == tileKey(p[1]) {
			t.Errorf("%q and %q should count as different links", p[0], p[1])
		}
	}
}

func TestMergeTiles(t *testing.T) {
	static := []Tile{
		{Name: "Harbor", URL: "https://harbor.example.com/"},
		{Name: "Gitea", URL: "https://gitea.example.com/"},
		{Name: "Gitea again", URL: "http://gitea.example.com"},
	}
	found := []Tile{
		{Name: "Argo", URL: "https://argo.example.com/", Source: "route argo/argo"},
		{Name: "harbor", URL: "https://harbor.example.com/", Source: "route harbor/harbor"},
		{Name: "Argo twice", URL: "https://ARGO.example.com", Source: "route argo/argo-2"},
	}
	got := mergeTiles(static, found)
	want := []string{"Harbor", "Gitea", "Argo"}
	if len(got) != len(want) {
		t.Fatalf("got %d tiles %+v, want %v", len(got), got, want)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("tile %d is %q, want %q", i, got[i].Name, name)
		}
	}
	if got[0].Source != "" {
		t.Errorf("the static Harbor tile should win over the discovered one, got source %q", got[0].Source)
	}
}

func TestRouteTiles(t *testing.T) {
	var routes httpRouteList
	err := json.Unmarshal([]byte(`{"items":[
	  {"metadata":{"name":"grafana","namespace":"mon","annotations":{"view.example.com/name":"Dashboards","view.example.com/icon":"https://example.com/g.png"}},
	   "spec":{"hostnames":["grafana.example.com"]}},
	  {"metadata":{"name":"wild","namespace":"apps"},"spec":{"hostnames":["*.apps.example.com"]}},
	  {"metadata":{"name":"deep","namespace":"apps","annotations":{"view.example.com/url":"https://example.com/deep/link"}},
	   "spec":{"hostnames":["*.apps.example.com"]}},
	  {"metadata":{"name":"bad","namespace":"apps","annotations":{"view.example.com/url":"not a url"}},
	   "spec":{"hostnames":["bad.example.com"]}},
	  {"metadata":{"name":"argo","namespace":"argo"},"spec":{"hostnames":["argo.example.com","argocd.example.com"]}}
	]}`), &routes)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DiscoveryConfig{Label: "view.example.com/enabled", Prefix: "view.example.com/"}
	got, skipped := routeTiles(routes, dc)
	if len(skipped) != 2 {
		t.Errorf("expected the wildcard and the bad url to be skipped, got %q", skipped)
	}

	want := []Tile{
		{Name: "Argo", URL: "https://argo.example.com/", Source: "route argo/argo"},
		{Name: "Dashboards", URL: "https://grafana.example.com/", Icon: "https://example.com/g.png", Source: "route mon/grafana"},
		{Name: "Example", URL: "https://example.com/deep/link", Source: "route apps/deep"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tiles %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tile %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

func TestDiscoveryConfig(t *testing.T) {
	b, err := parseConfig([]byte("discovery:\n  enabled: true\n"))
	if err != nil || b.Discovery == nil || b.Discovery.Label != defaultDiscoveryLabel || b.Discovery.Prefix != "view.glitchfactory.io/" {
		t.Fatalf("default label: %+v, %v", b.Discovery, err)
	}
	b, _ = parseConfig([]byte("discovery:\n  label: tiles.example.com/show\n"))
	if b.Discovery == nil || b.Discovery.Prefix != "tiles.example.com/" {
		t.Fatalf("custom label: %+v", b.Discovery)
	}
	b, _ = parseConfig([]byte("discovery:\n  enabled: false\n"))
	if b.Discovery != nil {
		t.Fatal("enabled: false should turn discovery off")
	}
	for _, bad := range []string{"discovery:\n  label: enabled\n", "discovery:\n  label: Example.com/x\n", "discovery:\n  lable: a.b/c\n"} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClusterConfig(t *testing.T) {
	ok := []struct {
		yaml string
		want *Cluster
	}{
		{"title: x\n", nil},
		{"cluster:\n  name: k8s-home\n  environment: staging\n", &Cluster{Name: "k8s-home", Environment: "staging"}},
		{"cluster:\n  name: \" k8s-home \"\n", &Cluster{Name: "k8s-home"}},
		{"cluster:\n  environment: prod\n", &Cluster{Environment: "prod"}},
		{"cluster:\n  name: \"\"\n", nil},
		{"cluster: {}\n", nil},
	}
	for _, c := range ok {
		b, err := parseConfig([]byte(c.yaml))
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.yaml, err)
			continue
		}
		if (b.Cluster == nil) != (c.want == nil) || (b.Cluster != nil && *b.Cluster != *c.want) {
			t.Errorf("%q: got %+v, want %+v", c.yaml, b.Cluster, c.want)
		}
	}

	bad := map[string]string{
		"cluster: k8s-home\n":                                        "cluster needs name and/or environment",
		"cluster:\n  nmae: k8s-home\n":                               "is not a cluster setting",
		"cluster:\n  name: |\n    two\n    lines\n":                  "single line",
		"cluster:\n  environment: " + strings.Repeat("x", 61) + "\n": "too long",
	}
	for y, want := range bad {
		_, err := parseConfig([]byte(y))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want one containing %q", y, err, want)
		}
	}
}

func TestThemeConfig(t *testing.T) {
	glitch := Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 6}
	ok := []struct {
		yaml string
		want *Theme
	}{
		{"title: x\n", nil},
		{"theme: {}\n", nil},
		{"theme:\n  name: standard\n", nil},
		// the glitch settings are kept in place while the standard look is on
		{"theme:\n  name: standard\n  shadow: 80\n  every: 2s\n", nil},
		{"theme:\n  name: glitch\n", &glitch},
		{"theme:\n  name: \" Glitch \"\n", &glitch},
		{"theme:\n  name: glitch\n  shadow: 0\n  feel: 100\n  coarse: 10\n  every: 90s\n", &Theme{Name: "glitch", Shadow: 0, Feel: 100, Coarse: 10, Every: 90}},
		{"theme:\n  name: glitch\n  every: 1500ms\n", &Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 1.5}},
		{"theme:\n  name: glitch\n  every: 2m\n", &Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 120}},
		{"theme:\n  name: glitch\n  every: 12\n", &Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 12}},
		{"theme:\n  name: glitch\n  every: 0\n", &Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 0}},
		{"theme:\n  name: glitch\n  every: 0s\n", &Theme{Name: "glitch", Shadow: 50, Feel: 30, Coarse: 65, Every: 0}},
	}
	for _, c := range ok {
		b, err := parseConfig([]byte(c.yaml))
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.yaml, err)
			continue
		}
		if (b.Theme == nil) != (c.want == nil) || (b.Theme != nil && *b.Theme != *c.want) {
			t.Errorf("%q: got %+v, want %+v", c.yaml, b.Theme, c.want)
		}
	}

	bad := map[string]string{
		"theme: glitch\n":                           "theme needs name",
		"theme:\n  name: neon\n":                    "should be standard or glitch",
		"theme:\n  nmae: glitch\n":                  "is not a theme setting",
		"theme:\n  name: glitch\n  shadow: 101\n":   "shadow 101 should be between 0 and 100",
		"theme:\n  name: glitch\n  feel: -1\n":      "feel -1 should be between 0 and 100",
		"theme:\n  name: standard\n  coarse: 500\n": "coarse 500 should be between 0 and 100",
		"theme:\n  name: glitch\n  every: often\n":  "every often should be a time",
		"theme:\n  name: glitch\n  every: 200ms\n":  "between 1s and 1h",
		"theme:\n  name: glitch\n  every: 2h\n":     "between 1s and 1h",
		"theme:\n  name: glitch\n  every: -6s\n":    "between 1s and 1h",
		"theme:\n  name: glitch\n  every: [1, 2]\n": "should be a time",
		"glitch:\n  every: 6s\n":                    "is not a known setting",
	}
	for y, want := range bad {
		_, err := parseConfig([]byte(y))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want one containing %q", y, err, want)
		}
	}

	// the standard look leaves the board as it was before themes existed
	b, _ := parseConfig([]byte("theme:\n  name: standard\n"))
	if js, _ := json.Marshal(b); strings.Contains(string(js), "theme") {
		t.Errorf("standard theme should not be sent to the page: %s", js)
	}
}

func TestServiceLinks(t *testing.T) {
	ok := []struct {
		svc  string
		want string
	}{
		{"url: https://nexus.example.com/service/rest/v1/status", "https://nexus.example.com/"},
		{"url: https://gitlab.example.com:8443/users/sign_in?x=1", "https://gitlab.example.com:8443/"},
		{"url: http://grafana.example.com/api/health", "http://grafana.example.com/"},
		{"url: https://nexus.example.com/status\n    link: https://nexus.example.com/#browse", "https://nexus.example.com/#browse"},
		{"url: https://nexus.example.com/status\n    link: nexus.example.com/ui", "https://nexus.example.com/ui"},
		{"url: https://www.example.com\n    link: false", ""},
		{"url: https://www.example.com\n    link: true", "https://www.example.com/"},
		{"url: https://www.example.com\n    link: \"\"", "https://www.example.com/"},
		{"tcp: ldap.example.com:636", ""},
		{"tcp: ldap.example.com:636\n    link: https://ldap-admin.example.com", "https://ldap-admin.example.com/"},
	}
	for _, c := range ok {
		y := "platform:\n  services:\n  - " + c.svc + "\n"
		b, err := parseConfig([]byte(y))
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.svc, err)
			continue
		}
		if got := b.PlatformCfg.Services[0].Link; got != c.want {
			t.Errorf("%q: link %q, want %q", c.svc, got, c.want)
		}
	}

	bad := map[string]string{
		"url: https://a.example.com\n    link: 42":                   "should be a web address",
		"url: https://a.example.com\n    link: ftp://a.example.com":  "is not a web address",
		"url: https://a.example.com\n    lnk: https://b.example.com": "use name, url, tcp, tls, expect, proxy or link",
	}
	for svc, want := range bad {
		_, err := parseConfig([]byte("platform:\n  services:\n  - " + svc + "\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want one containing %q", svc, err, want)
		}
	}
}

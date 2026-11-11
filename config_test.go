package main

import (
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

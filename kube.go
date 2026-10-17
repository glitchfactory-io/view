package main

// A tiny read-only client for the Kubernetes API, using only the standard library.
//
// Inside a pod, Kubernetes provides everything needed: the API address in
// environment variables, and a token plus CA certificate in a mounted folder.
// What the token may read is decided by the chart (charts/view/templates/rbac.yaml).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var (
	errNotInCluster = errors.New("not running inside Kubernetes")
	errNotFound     = errors.New("not found")
)

type kube struct {
	base      string
	tokenPath string // re-read on every request: Kubernetes rotates the token
	client    *http.Client
}

func newKube() (*kube, error) {
	// For local testing only: talk to a plain-http stand-in for the API.
	if u := os.Getenv("KUBE_API_URL"); u != "" {
		return &kube{base: strings.TrimRight(u, "/"), client: &http.Client{Timeout: 10 * time.Second}}, nil
	}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errNotInCluster
	}
	caPEM, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("can't read the cluster's CA certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &kube{
		base:      "https://" + net.JoinHostPort(host, port),
		tokenPath: saDir + "/token",
		client:    &http.Client{Transport: tr, Timeout: 10 * time.Second},
	}, nil
}

// get reads one API path into out. Errors are worded for the page.
func (k *kube) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if k.tokenPath != "" {
		tok, err := os.ReadFile(k.tokenPath)
		if err != nil {
			return fmt.Errorf("can't read the service account token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach the Kubernetes API: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("not allowed to read %s (check the service account's RBAC rules)", resourceOf(path))
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the Kubernetes API answered %s for %s", resp.Status, resourceOf(path))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// resourceOf turns "/apis/gateway.networking.k8s.io/v1/httproutes?labelSelector=…" into "httproutes".
func resourceOf(path string) string {
	path, _, _ = strings.Cut(path, "?")
	return path[strings.LastIndex(path, "/")+1:]
}

// ---------- the few fields we need ----------

type objectMeta struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
}

type gatewayList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Listeners []struct {
				Name     string `json:"name"`
				Hostname string `json:"hostname"`
				Port     int    `json:"port"`
				Protocol string `json:"protocol"`
			} `json:"listeners"`
		} `json:"spec"`
		Status struct {
			Addresses []struct {
				Value string `json:"value"`
			} `json:"addresses"`
		} `json:"status"`
	} `json:"items"`
}

type httpRouteList struct {
	Items []struct {
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Hostnames []string `json:"hostnames"`
		} `json:"spec"`
	} `json:"items"`
}

type nodeList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Status   struct {
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
		} `json:"status"`
	} `json:"items"`
}

type serviceList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Type string `json:"type"`
		} `json:"spec"`
		Status struct {
			LoadBalancer struct {
				Ingress []struct {
					IP string `json:"ip"`
				} `json:"ingress"`
			} `json:"loadBalancer"`
		} `json:"status"`
	} `json:"items"`
}

type leaseList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			HolderIdentity *string `json:"holderIdentity"`
		} `json:"spec"`
	} `json:"items"`
}

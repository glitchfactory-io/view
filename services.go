package main

// The services card: can things running in this cluster reach the services
// its users depend on (GitLab, Nexus, the proxy, ...)?
//
// Each service is checked every minute from inside the cluster, so the check
// takes the same path a pod would: node, firewall, proxy, service. A service
// only counts as down after two failures in a row, so a single blip doesn't
// flash red. Certificates are read along the way.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	slowAfter    = 2 * time.Second
	failsForDown = 2
)

type ServicesStatus struct {
	Checked time.Time       `json:"checked"`
	Items   []ServiceStatus `json:"items"`
}

type ServiceStatus struct {
	Name      string    `json:"name"`
	Target    string    `json:"target"`           // host or host:port, for the tooltip
	State     string    `json:"state"`            // checking, up, slow, down
	Detail    string    `json:"detail,omitempty"` // why it's down, or what's slow
	Since     time.Time `json:"since,omitzero"`   // down since
	Ms        int       `json:"ms,omitempty"`     // response time of the last check
	CertDays  *int      `json:"certDays,omitempty"`
	CertLevel string    `json:"certLevel,omitempty"` // warn or bad
}

type serviceWatcher struct {
	cfg   *config
	every time.Duration
	state map[string]*serviceState
}

type serviceState struct {
	everUp    bool
	fails     int
	firstFail time.Time
	lastErr   string
}

func newServiceWatcher(cfg *config) *serviceWatcher {
	every := time.Minute
	// For local testing only.
	if d, err := time.ParseDuration(os.Getenv("SERVICE_CHECK_EVERY")); err == nil && d >= time.Second {
		every = d
	}
	return &serviceWatcher{cfg: cfg, every: every, state: map[string]*serviceState{}}
}

func (w *serviceWatcher) run(ctx context.Context) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type checkResult struct {
	err      error
	elapsed  time.Duration
	certDays int // -1 when there was no certificate
}

func (w *serviceWatcher) tick(ctx context.Context) {
	pc := w.cfg.platformConfig()
	if pc == nil || len(pc.Services) == 0 {
		w.cfg.setServices(nil)
		w.state = map[string]*serviceState{}
		return
	}

	// Show the card straight away on the first round, before the slowest check returns.
	if len(w.state) == 0 {
		items := make([]ServiceStatus, len(pc.Services))
		for i, s := range pc.Services {
			items[i] = ServiceStatus{Name: s.Name, Target: targetOf(s), State: "checking"}
		}
		w.cfg.setServices(&ServicesStatus{Checked: time.Now().UTC().Truncate(time.Second), Items: items})
	}

	results := make([]checkResult, len(pc.Services))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, s := range pc.Services {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, s ServiceCheck) {
			defer func() { <-sem; wg.Done() }()
			if s.URL != "" {
				results[i] = checkURL(ctx, s)
			} else {
				results[i] = checkTCP(ctx, s)
			}
		}(i, s)
	}
	wg.Wait()

	now := time.Now().UTC().Truncate(time.Second)
	keep := map[string]*serviceState{}
	items := make([]ServiceStatus, len(pc.Services))
	for i, s := range pc.Services {
		key := s.Name + "|" + s.URL + s.TCP + "|" + s.Proxy
		st := w.state[key]
		if st == nil {
			st = &serviceState{}
		}
		keep[key] = st
		r := results[i]
		it := ServiceStatus{Name: s.Name, Target: targetOf(s), Ms: int(r.elapsed / time.Millisecond)}

		if r.err == nil {
			if st.fails >= failsForDown {
				log.Printf("services card: %s is back up", s.Name)
			}
			st.everUp, st.fails, st.lastErr = true, 0, ""
			it.State = "up"
			if r.elapsed > slowAfter {
				it.State = "slow"
				it.Detail = fmt.Sprintf("took %.1f s", r.elapsed.Seconds())
			}
		} else {
			st.fails++
			if st.fails == 1 {
				st.firstFail = now
			}
			st.lastErr = r.err.Error()
			switch {
			case st.fails >= failsForDown:
				if st.fails == failsForDown {
					log.Printf("services card: %s is down: %s", s.Name, st.lastErr)
				}
				it.State, it.Detail, it.Since = "down", st.lastErr, st.firstFail
			case st.everUp:
				it.State = "up" // one failure isn't enough to call it down
			default:
				it.State = "checking"
			}
			it.Ms = 0
		}
		if r.err == nil && r.certDays >= 0 {
			d := r.certDays
			it.CertDays = &d
			switch {
			case d < certBadDays:
				it.CertLevel = "bad"
			case d < pc.CertWarnDays:
				it.CertLevel = "warn"
			}
		}
		items[i] = it
	}
	w.state = keep
	w.cfg.setServices(&ServicesStatus{Checked: now, Items: items})
}

const certBadDays = 7 // below this a certificate is shown in red rather than amber

func targetOf(s ServiceCheck) string {
	if s.TCP != "" {
		return s.TCP
	}
	if u, err := url.Parse(s.URL); err == nil {
		return u.Host
	}
	return s.URL
}

// checkURL fetches the address the way a pod would. Any answer below 500
// counts as up (a login page or a 401 still proves the path works), unless
// the service asks for a specific status.
func checkURL(ctx context.Context, s ServiceCheck) checkResult {
	proxy := http.ProxyFromEnvironment
	switch s.Proxy {
	case "":
	case "none":
		proxy = nil
	default:
		pu, _ := url.Parse(s.Proxy)
		proxy = http.ProxyURL(pu)
	}
	tr := &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:     true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return checkResult{err: err, certDays: -1}
	}
	req.Header.Set("User-Agent", "view-service-check/1.0")
	viaProxy := false
	if proxy != nil {
		if pu, _ := proxy(req); pu != nil {
			viaProxy = true
		}
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return checkResult{err: explain(err, viaProxy), elapsed: elapsed, certDays: -1}
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()

	r := checkResult{elapsed: elapsed, certDays: -1}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.certDays = daysLeft(resp.TLS.PeerCertificates[0])
	}
	switch {
	case s.Expect != 0 && resp.StatusCode != s.Expect:
		r.err = fmt.Errorf("answered %d, expected %d", resp.StatusCode, s.Expect)
	case s.Expect == 0 && resp.StatusCode >= 500:
		if viaProxy && (resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504) {
			r.err = fmt.Errorf("answered %d (the proxy may not have reached it)", resp.StatusCode)
		} else {
			r.err = fmt.Errorf("answered %d", resp.StatusCode)
		}
	}
	return r
}

// checkTCP connects to host:port, and with tls also checks the certificate.
func checkTCP(ctx context.Context, s ServiceCheck) checkResult {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(cctx, "tcp", s.TCP)
	if err != nil {
		return checkResult{err: explain(err, false), elapsed: time.Since(start), certDays: -1}
	}
	defer conn.Close()
	r := checkResult{certDays: -1}
	if s.TLS {
		host, _, _ := net.SplitHostPort(s.TCP)
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(cctx); err != nil {
			return checkResult{err: explain(err, false), elapsed: time.Since(start), certDays: -1}
		}
		if certs := tc.ConnectionState().PeerCertificates; len(certs) > 0 {
			r.certDays = daysLeft(certs[0])
		}
	}
	r.elapsed = time.Since(start)
	return r
}

func daysLeft(c *x509.Certificate) int {
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// explain turns a connection error into a short reason for the card.
func explain(err error, viaProxy bool) error {
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var dnsErr *net.DNSError
	var netErr net.Error
	var opErr *net.OpError
	msg := err.Error()
	switch {
	case strings.Contains(msg, "proxyconnect"):
		if errors.As(err, &netErr) && netErr.Timeout() {
			return errors.New("the proxy didn't answer")
		}
		return errors.New("couldn't connect to the proxy")
	case errors.As(err, &hostErr):
		return errors.New("its certificate doesn't cover this name")
	case errors.As(err, &authErr):
		return errors.New("its certificate isn't from a trusted issuer")
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return errors.New("its certificate has expired")
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return errors.New("the name doesn't resolve")
	case errors.As(err, &netErr) && netErr.Timeout():
		return errors.New("no answer in time")
	case strings.Contains(msg, "connection refused"):
		return errors.New("connection refused")
	case strings.Contains(msg, "no route to host") || strings.Contains(msg, "network is unreachable"):
		return errors.New("no network route to it")
	case strings.Contains(msg, "connection reset"):
		return errors.New("the connection was reset")
	case viaProxy && !errors.As(err, &opErr):
		// The proxy answered the CONNECT request with something other than "OK".
		var ue *url.Error
		if errors.As(err, &ue) {
			msg = ue.Err.Error()
		}
		return fmt.Errorf("the proxy wouldn't connect it (%s)", msg)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	return errors.New(msg)
}

package main

// Reads the tile list from a YAML file and watches it for changes.
//
// Kubernetes updates a mounted ConfigMap by swapping the file behind a
// symlink, so we simply re-read the file every few seconds and compare.
// If an edit breaks the file, the last working tiles stay on screen and
// the page shows what's wrong.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // timezone data built in, so "Europe/Berlin" works in any base image

	"gopkg.in/yaml.v3"
)

type Tile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Icon   string `json:"icon,omitempty"`
	Source string `json:"source,omitempty"` // e.g. "route monitoring/grafana" for discovered tiles
}

// DiscoveryConfig turns HTTPRoutes carrying Label="true" into tiles (see discovery.go).
type DiscoveryConfig struct {
	Label  string // e.g. view.glitchfactory.io/enabled
	Prefix string // annotation prefix taken from the label, e.g. view.glitchfactory.io/
}

// Maintenance is a weekly window. The page works out the next one itself.
type Maintenance struct {
	Weekday  int    `json:"weekday"`  // 0 = Sunday … 6 = Saturday
	Start    int    `json:"start"`    // minutes after midnight
	End      int    `json:"end"`      // minutes after midnight; at or before Start = ends the next day
	Timezone string `json:"timezone"` // e.g. Europe/Berlin
}

// PlatformConfig is the platform block of the tile list. It stays on the server.
type PlatformConfig struct {
	Network      bool           // show the network card (network.go)
	CertWarnDays int            // warn when a service's certificate has fewer days left
	Services     []ServiceCheck // checked by services.go
}

// ServiceCheck is one external service the cluster's users depend on.
type ServiceCheck struct {
	Name   string
	URL    string // a web address to fetch, or
	TCP    string // host:port to connect to
	TLS    bool   // tcp only: also do a TLS handshake and check the certificate
	Expect int    // url only: the status that counts as up; 0 = any answer below 500
	Proxy  string // url only: "" = the pod's proxy settings, "none" = direct, or a proxy address
}

type Board struct {
	Title       string           `json:"title"`
	Tiles       []Tile           `json:"tiles"`
	Maintenance *Maintenance     `json:"maintenance,omitempty"`
	Error       string           `json:"error,omitempty"`
	PlatformCfg *PlatformConfig  `json:"-"`
	Discovery   *DiscoveryConfig `json:"-"`
}

type rawTile struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Icon string `yaml:"icon"`
}

type rawMaintenance struct {
	Day      string `yaml:"day"`
	From     string `yaml:"from"`
	To       string `yaml:"to"`
	Timezone string `yaml:"timezone"`
}

type rawService struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	TCP    string `yaml:"tcp"`
	TLS    bool   `yaml:"tls"`
	Expect int    `yaml:"expect"`
	Proxy  string `yaml:"proxy"`
}

type rawPlatform struct {
	Network      *bool        `yaml:"network"`
	CertWarnDays *int         `yaml:"certWarnDays"`
	Services     []rawService `yaml:"services"`
}

type rawDiscovery struct {
	Enabled *bool  `yaml:"enabled"`
	Label   string `yaml:"label"`
}

type rawConfig struct {
	Title       string          `yaml:"title"`
	Tiles       []rawTile       `yaml:"tiles"`
	Maintenance *rawMaintenance `yaml:"maintenance"`
	Platform    *rawPlatform    `yaml:"platform"`
	Discovery   *rawDiscovery   `yaml:"discovery"`
}

const defaultDiscoveryLabel = "view.glitchfactory.io/enabled"

// A Kubernetes label key with a prefix, like example.com/enabled.
var labelKeyRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)

func parseDiscovery(r *rawDiscovery) (*DiscoveryConfig, error) {
	if r.Enabled != nil && !*r.Enabled {
		return nil, nil
	}
	label := strings.TrimSpace(r.Label)
	if label == "" {
		label = defaultDiscoveryLabel
	}
	if !labelKeyRe.MatchString(label) || len(label) > 316 {
		return nil, fmt.Errorf("discovery: label %q should look like example.com/enabled", r.Label)
	}
	return &DiscoveryConfig{Label: label, Prefix: label[:strings.Index(label, "/")+1]}, nil
}

// tileKey identifies a link for de-duplication: host and path, ignoring
// http vs https, default ports, upper case and a trailing slash.
func tileKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		host += ":" + p
	}
	k := host + strings.TrimSuffix(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// mergeTiles puts the static tiles first, then discovered ones, each link once.
// A static tile always wins, so its name, icon and position are kept.
func mergeTiles(static, found []Tile) []Tile {
	out := make([]Tile, 0, len(static)+len(found))
	seen := map[string]bool{}
	for _, list := range [][]Tile{static, found} {
		for _, t := range list {
			k := tileKey(t.URL)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

func parsePlatform(r *rawPlatform) (*PlatformConfig, error) {
	pc := &PlatformConfig{CertWarnDays: 21}
	if r.CertWarnDays != nil {
		if *r.CertWarnDays < 1 || *r.CertWarnDays > 365 {
			return nil, fmt.Errorf("platform: certWarnDays %d should be between 1 and 365", *r.CertWarnDays)
		}
		pc.CertWarnDays = *r.CertWarnDays
	}
	pc.Network = r.Network == nil || *r.Network
	if len(r.Services) > 50 {
		return nil, errors.New("platform: too many services (50 at most)")
	}
	seen := map[string]bool{}
	for i, rs := range r.Services {
		sc, err := parseService(rs)
		if err != nil {
			label := strings.TrimSpace(rs.Name)
			if label == "" {
				label = fmt.Sprintf("number %d", i+1)
			}
			return nil, fmt.Errorf("platform: service %s: %v", label, err)
		}
		if seen[sc.Name] {
			return nil, fmt.Errorf("platform: there are two services called %q", sc.Name)
		}
		seen[sc.Name] = true
		pc.Services = append(pc.Services, sc)
	}
	return pc, nil
}

func parseService(r rawService) (ServiceCheck, error) {
	sc := ServiceCheck{Name: strings.TrimSpace(r.Name), TLS: r.TLS, Expect: r.Expect}
	u, t := strings.TrimSpace(r.URL), strings.TrimSpace(r.TCP)
	switch {
	case (u == "") == (t == ""):
		return sc, errors.New("set either url (for web services) or tcp (host:port)")
	case u != "":
		n, ok := normalizeURL(u)
		if !ok {
			return sc, fmt.Errorf("url %q is not a web address", r.URL)
		}
		sc.URL = n
		if r.TLS {
			return sc, errors.New("tls only applies to tcp checks (https addresses are always checked)")
		}
		if r.Expect != 0 && (r.Expect < 100 || r.Expect > 599) {
			return sc, fmt.Errorf("expect %d should be an HTTP status like 200", r.Expect)
		}
		switch p := strings.TrimSpace(r.Proxy); {
		case p == "" || strings.EqualFold(p, "none"):
			sc.Proxy = strings.ToLower(p)
		default:
			pu, err := url.Parse(p)
			if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
				return sc, fmt.Errorf("proxy %q should be an address like http://proxy.example.com:3128, or none", r.Proxy)
			}
			sc.Proxy = p
		}
		if sc.Name == "" {
			sc.Name = nameFromURL(n)
		}
	default:
		host, port, err := net.SplitHostPort(t)
		pn, perr := strconv.Atoi(port)
		if err != nil || host == "" || perr != nil || pn < 1 || pn > 65535 {
			return sc, fmt.Errorf("tcp %q should be host:port, like ldap.example.com:636", r.TCP)
		}
		if r.Expect != 0 || strings.TrimSpace(r.Proxy) != "" {
			return sc, errors.New("expect and proxy only apply to url checks")
		}
		sc.TCP = t
		if sc.Name == "" {
			sc.Name = host
		}
	}
	return sc, nil
}

var (
	weekdays = map[string]int{
		"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6,
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
	clockRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

// clock turns "18:00" into minutes after midnight. "24:00" is allowed as an end time.
func clock(s string, allow24 bool) (int, bool) {
	m := clockRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if mi > 59 || h > 24 || (h == 24 && (!allow24 || mi != 0)) {
		return 0, false
	}
	return h*60 + mi, true
}

func parseMaintenance(r *rawMaintenance) (*Maintenance, error) {
	day, ok := weekdays[strings.ToLower(strings.TrimSpace(r.Day))]
	if !ok {
		return nil, fmt.Errorf("maintenance: day %q should be a weekday like tuesday", r.Day)
	}
	start, ok := clock(r.From, false)
	if !ok {
		return nil, fmt.Errorf("maintenance: from %q should be a time like 18:00", r.From)
	}
	end, ok := clock(r.To, true)
	if !ok {
		return nil, fmt.Errorf("maintenance: to %q should be a time like 22:00", r.To)
	}
	if end == start {
		return nil, errors.New("maintenance: from and to are the same time")
	}
	tz := strings.TrimSpace(r.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		return nil, fmt.Errorf("maintenance: unknown timezone %q (use a name like Europe/Berlin)", r.Timezone)
	}
	return &Maintenance{Weekday: day, Start: start, End: end, Timezone: tz}, nil
}

var (
	hostRe   = regexp.MustCompile(`(?i)^([a-z0-9-]+\.)*[a-z0-9-]+$|^\[[0-9a-f:.]+\]$`)
	schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
	ipRe     = regexp.MustCompile(`^[\d.]+$`)
)

// normalizeURL accepts "grafana.lan", "10.0.0.5:3000" or a full http(s) address.
func normalizeURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t\r\n") || len(s) > 2048 {
		return "", false
	}
	if !schemeRe.MatchString(s) {
		s = "https://" + strings.TrimLeft(s, "/")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !hostRe.MatchString(u.Hostname()) {
		return "", false
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	h := strings.TrimPrefix(u.Hostname(), "www.")
	if ipRe.MatchString(h) || strings.Contains(h, ":") {
		return h
	}
	first := strings.SplitN(h, ".", 2)[0]
	if first == "" {
		return h
	}
	return strings.ToUpper(first[:1]) + first[1:]
}

func friendlyYAMLError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n")
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.ReplaceAll(msg, "not found in type main.rawTile", "is not a tile setting (use name, url or icon)")
	msg = strings.ReplaceAll(msg, "not found in type main.rawConfig", "is not a known setting (use title, tiles, maintenance, platform or discovery)")
	msg = strings.ReplaceAll(msg, "not found in type main.rawDiscovery", "is not a discovery setting (use enabled or label)")
	msg = strings.ReplaceAll(msg, "not found in type main.rawPlatform", "is not a platform setting (use network, certWarnDays or services)")
	msg = strings.ReplaceAll(msg, "not found in type main.rawService", "is not a service setting (use name, url, tcp, tls, expect or proxy)")
	msg = strings.ReplaceAll(msg, "not found in type main.rawMaintenance", "is not a maintenance setting (use day, from, to or timezone)")
	msg = regexp.MustCompile(`cannot unmarshal !!\w+ `+"`"+`([^`+"`"+`]*)`+"`"+` into \[\]main\.rawTile`).ReplaceAllString(msg, "tiles must be a list, each entry starting with \"- \"")
	msg = regexp.MustCompile(`cannot unmarshal !!\w+ `+"`"+`?([^`+"`"+`]*)`+"`"+`? into main\.rawTile`).ReplaceAllString(msg, "each tile needs \"name:\" and \"url:\" on their own lines")
	if strings.Contains(msg, "did not find expected") || strings.Contains(msg, "mapping values are not allowed") {
		msg += " (usually the spaces at the start of a line don't line up with the lines around it)"
	}
	msg = regexp.MustCompile(`cannot unmarshal !!\w+ `+"`"+`?([^`+"`"+`]*)`+"`"+`? into main\.rawMaintenance`).ReplaceAllString(msg, "maintenance needs day, from, to and timezone, each on its own line below it")
	lines := strings.Split(msg, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "; ")
}

func parseConfig(data []byte) (Board, error) {
	b := Board{Title: "view", Tiles: []Tile{}}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var rc rawConfig
	if err := dec.Decode(&rc); err != nil && !errors.Is(err, io.EOF) {
		return b, errors.New(friendlyYAMLError(err))
	}
	// line numbers for clearer messages
	var lines struct {
		Tiles []yaml.Node `yaml:"tiles"`
	}
	yaml.Unmarshal(data, &lines)
	lineOf := func(i int) string {
		if i < len(lines.Tiles) {
			return fmt.Sprintf("line %d", lines.Tiles[i].Line)
		}
		return fmt.Sprintf("tile %d", i+1)
	}

	if t := strings.TrimSpace(rc.Title); t != "" {
		b.Title = t
	}
	if len(rc.Tiles) > 500 {
		return b, errors.New("too many tiles (500 at most)")
	}
	for i, rt := range rc.Tiles {
		if strings.TrimSpace(rt.URL) == "" {
			return b, fmt.Errorf("%s: this tile has no url", lineOf(i))
		}
		u, ok := normalizeURL(rt.URL)
		if !ok {
			return b, fmt.Errorf("%s: %q is not a web address", lineOf(i), rt.URL)
		}
		t := Tile{Name: strings.TrimSpace(rt.Name), URL: u}
		if t.Name == "" {
			t.Name = nameFromURL(u)
		}
		if ic := strings.TrimSpace(rt.Icon); ic != "" {
			iu, ok := normalizeURL(ic)
			if !ok {
				return b, fmt.Errorf("%s: icon %q is not a web address", lineOf(i), rt.Icon)
			}
			t.Icon = iu
		}
		b.Tiles = append(b.Tiles, t)
	}
	if rc.Maintenance != nil {
		m, err := parseMaintenance(rc.Maintenance)
		if err != nil {
			return b, err
		}
		b.Maintenance = m
	}
	if rc.Discovery != nil {
		d, err := parseDiscovery(rc.Discovery)
		if err != nil {
			return b, err
		}
		b.Discovery = d
	}
	if rc.Platform != nil {
		pc, err := parsePlatform(rc.Platform)
		if err != nil {
			return b, err
		}
		b.PlatformCfg = pc
	}
	return b, nil
}

// ---------- watching + live updates ----------

type config struct {
	path string

	mu       sync.Mutex
	stateKey string          // file contents or read error; used to skip unchanged files
	good     *Board          // last version without mistakes
	board    Board           // what's shown now (the last good tiles, plus any problem)
	network  *NetworkStatus  // latest network card, nil when it's off
	found    []Tile          // tiles discovered from labelled routes
	foundErr string          // why discovery isn't working, if it isn't
	services *ServicesStatus // latest service checks, nil when there are none
	snap     []byte          // what the browser gets
	subs     map[chan []byte]struct{}
}

func newConfig(path string) *config {
	c := &config{path: path, subs: map[chan []byte]struct{}{}}
	c.reload()
	return c
}

func (c *config) reload() {
	data, readErr := os.ReadFile(c.path)
	key := "ok:" + string(data)
	if readErr != nil {
		key = "err:" + readErr.Error()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.stateKey {
		return
	}
	c.stateKey = key

	var board Board
	var problem string
	switch {
	case readErr != nil && errors.Is(readErr, os.ErrNotExist):
		problem = "can't find the tile list at " + c.path
	case readErr != nil:
		problem = "can't read the tile list: " + readErr.Error()
	default:
		b, err := parseConfig(data)
		if err != nil {
			problem = err.Error()
		} else {
			board = b
			c.good = &b
		}
	}
	if problem != "" {
		if c.good != nil {
			board = *c.good
		} else {
			board = Board{Title: "view", Tiles: []Tile{}}
		}
		board.Error = problem
		log.Printf("tile list has a problem: %s", problem)
	} else {
		log.Printf("tile list loaded: %d sites", len(board.Tiles))
	}

	c.board = board
	c.publishLocked()
}

// publishLocked sends the board and sidebar status to every open page, if anything changed.
func (c *config) publishLocked() {
	shown := c.board
	shown.Tiles = mergeTiles(c.board.Tiles, c.found)
	snap, _ := json.Marshal(struct {
		Board
		DiscoveryError string          `json:"discoveryError,omitempty"`
		Network        *NetworkStatus  `json:"network,omitempty"`
		Services       *ServicesStatus `json:"services,omitempty"`
	}{shown, c.foundErr, c.network, c.services})
	if bytes.Equal(snap, c.snap) {
		return
	}
	c.snap = snap
	for ch := range c.subs {
		select {
		case ch <- snap:
		default: // browser is behind: replace its pending update with the newest one
			select {
			case <-ch:
			default:
			}
			ch <- snap
		}
	}
}

func (c *config) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.reload()
		}
	}
}

// platformConfig is the platform block of the tile list in use, or nil.
func (c *config) platformConfig() *PlatformConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.board.PlatformCfg
}

func (c *config) networkOn() bool {
	pc := c.platformConfig()
	return pc != nil && pc.Network
}

func (c *config) setNetwork(n *NetworkStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.network = n
	c.publishLocked()
}

func (c *config) discoveryConfig() *DiscoveryConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.board.Discovery
}

func (c *config) setDiscovered(tiles []Tile, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.found, c.foundErr = tiles, errMsg
	c.publishLocked()
}

func (c *config) setServices(s *ServicesStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services = s
	c.publishLocked()
}

func (c *config) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

func (c *config) subscribe() (chan []byte, []byte) {
	ch := make(chan []byte, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[ch] = struct{}{}
	return ch, c.snap
}

func (c *config) unsubscribe(ch chan []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, ch)
}

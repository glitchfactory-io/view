package main

// Auto-tiles: HTTPRoutes labelled with the discovery label (by default
// view.glitchfactory.io/enabled: "true") become tiles, next to the static ones.
//
// A route can fine-tune its tile with annotations under the label's prefix:
//
//	view.glitchfactory.io/name: Grafana
//	view.glitchfactory.io/icon: https://example.com/logo.png
//	view.glitchfactory.io/url:  https://grafana.example.com/d/home
//
// Without them the tile links to https://<first hostname>/ and is named after it.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type discoveryWatcher struct {
	cfg     *config
	kube    *kube
	kubeErr error
	every   time.Duration
	lastLog string // what was logged last, so an unchanged state isn't logged every round

	// The last tiles that were read successfully, kept through a failed read
	// so a blip in the API doesn't make them vanish and come back.
	kept      []Tile
	keptLabel string
}

func newDiscoveryWatcher(cfg *config) *discoveryWatcher {
	k, err := newKube()
	every := 30 * time.Second
	// For local testing only.
	if d, err := time.ParseDuration(os.Getenv("DISCOVERY_EVERY")); err == nil && d >= time.Second {
		every = d
	}
	return &discoveryWatcher{cfg: cfg, kube: k, kubeErr: err, every: every}
}

func (d *discoveryWatcher) run(ctx context.Context) {
	t := time.NewTicker(d.every)
	defer t.Stop()
	for {
		d.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// report hands the tiles to the page, and logs what happened when it changed.
func (d *discoveryWatcher) report(tiles []Tile, skipped []string, problem string) {
	lines := []string{"auto-tiles: " + problem}
	if problem == "" {
		lines = []string{"auto-tiles: labelled routes in use: " + strconv.Itoa(len(tiles))}
		for _, s := range skipped {
			lines = append(lines, "auto-tiles: skipped "+s)
		}
	}
	if msg := strings.Join(lines, "\n"); msg != d.lastLog {
		for _, l := range lines {
			log.Print(l)
		}
		d.lastLog = msg
	}
	d.cfg.setDiscovered(tiles, problem)
}

func (d *discoveryWatcher) tick(parent context.Context) {
	dc := d.cfg.discoveryConfig()
	if dc == nil {
		d.lastLog, d.kept, d.keptLabel = "", nil, ""
		d.cfg.setDiscovered(nil, "")
		return
	}
	if dc.Label != d.keptLabel {
		d.kept = nil // those were found with a different label
	}
	if d.kube == nil {
		d.report(nil, nil, d.kubeErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	var routes httpRouteList
	path := "/apis/gateway.networking.k8s.io/v1/httproutes?labelSelector=" + url.QueryEscape(dc.Label+"=true")
	if err := d.kube.get(ctx, path, &routes); err != nil {
		if errors.Is(err, errNotFound) {
			err = errors.New("the Gateway API isn't installed in this cluster")
		}
		d.report(d.kept, nil, err.Error())
		return
	}
	tiles, skipped := routeTiles(routes, dc)
	d.kept, d.keptLabel = tiles, dc.Label
	d.report(tiles, skipped, "")
}

// routeTiles turns labelled routes into tiles, sorted by name. Routes that
// can't become a tile are listed in skipped, with the reason.
func routeTiles(routes httpRouteList, dc *DiscoveryConfig) (tiles []Tile, skipped []string) {
	tiles = []Tile{}
	for _, r := range routes.Items {
		ann := r.Metadata.Annotations
		source := "route " + r.Metadata.Namespace + "/" + r.Metadata.Name

		link := ""
		if v := strings.TrimSpace(ann[dc.Prefix+"url"]); v != "" {
			u, ok := normalizeURL(v)
			if !ok {
				skipped = append(skipped, fmt.Sprintf("%s: its %surl annotation %q is not a web address", source, dc.Prefix, v))
				continue
			}
			link = u
		} else {
			for _, h := range r.Spec.Hostnames {
				h = strings.TrimSuffix(strings.TrimSpace(h), ".")
				if h != "" && !strings.HasPrefix(h, "*") {
					link, _ = normalizeURL("https://" + h + "/")
					break
				}
			}
			if link == "" {
				skipped = append(skipped, fmt.Sprintf("%s: no hostname to link to (wildcards don't count), set the %surl annotation", source, dc.Prefix))
				continue
			}
		}

		t := Tile{URL: link, Name: strings.TrimSpace(ann[dc.Prefix+"name"]), Source: source}
		if t.Name == "" {
			t.Name = nameFromURL(link)
		}
		if len([]rune(t.Name)) > 80 {
			t.Name = string([]rune(t.Name)[:80])
		}
		if v := strings.TrimSpace(ann[dc.Prefix+"icon"]); v != "" {
			if u, ok := normalizeURL(v); ok {
				t.Icon = u
			}
		}
		tiles = append(tiles, t)
	}
	sort.SliceStable(tiles, func(i, j int) bool {
		return strings.ToLower(tiles[i].Name) < strings.ToLower(tiles[j].Name)
	})
	return tiles, skipped
}

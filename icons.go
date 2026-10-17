package main

// Finds and caches the icon of each site, so tiles show real logos.
// The server does the fetching (not the browser) because it can reach
// internal services, copes with self-signed certificates, and keeps a
// copy on disk so the page stays fast.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxIconBytes = 1 << 20
	missRetry    = time.Hour // after a failed lookup, try again after this long
)

var errNoIcon = errors.New("no icon")

type iconCache struct {
	dir    string
	client *http.Client
	locks  sync.Map // key -> *sync.Mutex
}

func newIconCache(dir string) *iconCache {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Home-lab services often use self-signed certificates. We only ever
	// read public icons through this client, so skipping verification is fine.
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &iconCache{
		dir: dir,
		client: &http.Client{
			Transport: tr,
			Timeout:   6 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

func (c *iconCache) get(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", errNoIcon
	}
	sum := sha256.Sum256([]byte(u.String()))
	key := hex.EncodeToString(sum[:12])
	imgPath := filepath.Join(c.dir, key+".img")
	typePath := filepath.Join(c.dir, key+".type")
	missPath := filepath.Join(c.dir, key+".miss")

	m, _ := c.locks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	if data, err := os.ReadFile(imgPath); err == nil {
		if t, err := os.ReadFile(typePath); err == nil {
			return data, string(t), nil
		}
	}
	if fi, err := os.Stat(missPath); err == nil && time.Since(fi.ModTime()) < missRetry {
		return nil, "", errNoIcon
	}

	// Finish the lookup even if the browser gives up, so the result is cached.
	data, ctype, err := c.find(context.WithoutCancel(ctx), u)
	if err != nil {
		os.WriteFile(missPath, nil, 0o644)
		return nil, "", errNoIcon
	}
	os.Remove(missPath)
	os.WriteFile(imgPath, data, 0o644)
	os.WriteFile(typePath, []byte(ctype), 0o644)
	return data, ctype, nil
}

type candidate struct {
	href  string
	score int
}

var (
	linkTag  = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrPair = regexp.MustCompile(`(?is)([a-z][a-z0-9_:-]*)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)
	sizeNum  = regexp.MustCompile(`(\d+)x\d+`)
)

func (c *iconCache) find(ctx context.Context, page *url.URL) ([]byte, string, error) {
	var cands []candidate
	base := page

	if body, final, err := c.fetch(ctx, page.String(), 2<<20); err == nil {
		base = final
		// The address may point straight at an image (a custom "icon:" in the config).
		if ctype, ok := imageType(body); ok && len(body) <= maxIconBytes {
			return body, ctype, nil
		}
		for _, tag := range linkTag.FindAllString(string(body), -1) {
			attrs := map[string]string{}
			for _, m := range attrPair.FindAllStringSubmatch(tag, -1) {
				attrs[strings.ToLower(m[1])] = strings.Trim(m[2], `"'`)
			}
			rel := " " + strings.ToLower(attrs["rel"]) + " "
			href := strings.TrimSpace(attrs["href"])
			if href == "" || !strings.Contains(rel, "icon") || strings.Contains(rel, "mask-icon") {
				continue
			}
			score := 32
			if strings.Contains(rel, "apple-touch-icon") {
				score = 180
			}
			for _, m := range sizeNum.FindAllStringSubmatch(strings.ToLower(attrs["sizes"]), -1) {
				if n, _ := strconv.Atoi(m[1]); n > score || score == 32 {
					score = n
				}
			}
			if strings.Contains(attrs["type"], "svg") || strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".svg") || strings.Contains(strings.ToLower(attrs["sizes"]), "any") {
				score = 400
			}
			if score > 512 {
				score = 512 - score/1000 // prefer around 180–512 px, not huge banners
			}
			cands = append(cands, candidate{href, score})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	cands = append(cands, candidate{"/apple-touch-icon.png", 0}, candidate{"/favicon.ico", 0})

	tried := map[string]bool{}
	for _, cd := range cands {
		if strings.HasPrefix(cd.href, "data:") {
			if data, ctype, ok := decodeDataURI(cd.href); ok {
				return data, ctype, nil
			}
			continue
		}
		ref, err := url.Parse(cd.href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if (abs.Scheme != "http" && abs.Scheme != "https") || tried[abs.String()] {
			continue
		}
		tried[abs.String()] = true
		body, _, err := c.fetch(ctx, abs.String(), maxIconBytes)
		if err != nil || len(body) == 0 {
			continue
		}
		if ctype, ok := imageType(body); ok {
			return body, ctype, nil
		}
	}
	return nil, "", errNoIcon
}

func (c *iconCache) fetch(ctx context.Context, target string, limit int64) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; view-startpage/1.0)")
	req.Header.Set("Accept", "text/html,image/*;q=0.9,*/*;q=0.5")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errors.New(resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > limit {
		if limit == maxIconBytes {
			return nil, nil, errors.New("icon too large")
		}
		body = body[:limit] // a long HTML page: the <head> is all we need
	}
	return body, resp.Request.URL, nil
}

func imageType(b []byte) (string, bool) {
	ct := http.DetectContentType(b)
	if strings.HasPrefix(ct, "image/") {
		return ct, true
	}
	head := bytes.ToLower(b[:min(len(b), 1024)])
	if bytes.Contains(head, []byte("<html")) || bytes.Contains(head, []byte("<!doctype html")) {
		return "", false // a web page that merely contains an inline <svg>
	}
	if bytes.Contains(head, []byte("<svg")) {
		return "image/svg+xml", true
	}
	return "", false
}

func decodeDataURI(s string) ([]byte, string, bool) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	if !ok {
		return nil, "", false
	}
	var data []byte
	if strings.HasSuffix(meta, ";base64") {
		d, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, "", false
		}
		data = d
	} else {
		d, err := url.PathUnescape(payload)
		if err != nil {
			return nil, "", false
		}
		data = []byte(d)
	}
	if len(data) == 0 || len(data) > maxIconBytes {
		return nil, "", false
	}
	ct, ok := imageType(data)
	return data, ct, ok
}

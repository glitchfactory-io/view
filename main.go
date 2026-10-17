// view: a tiny, read-only personal start page.
//
// The tiles come from a YAML file (in Kubernetes: a ConfigMap). The server
// checks that file every couple of seconds and pushes changes straight to
// any open browser, so edits show up without restarts or page reloads.
package main

import (
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type server struct {
	cfg   *config
	icons *iconCache
	page  string // index.html with a /*BOARD*/ placeholder
	quit  chan struct{}
}

func gz(w http.ResponseWriter, r *http.Request, ctype string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Vary", "Accept-Encoding")
	if len(body) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		zw.Write(body)
		zw.Close()
		return
	}
	w.Write(body)
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// The board JSON is escaped by json.Marshal (<, >, &), so it is safe inside a <script> tag.
	body := strings.Replace(s.page, "/*BOARD*/", string(s.cfg.snapshot()), 1)
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	gz(w, r, "text/html; charset=utf-8", []byte(body))
}

func (s *server) board(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		http.Error(w, "this page is read-only; change the tiles in the config file", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	gz(w, r, "application/json", s.cfg.snapshot())
}

// events keeps a connection open and sends the board every time the config changes.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // this connection stays open on purpose
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")

	ch, current := s.cfg.subscribe()
	defer s.cfg.unsubscribe(ch)

	send := func(b []byte) bool {
		if _, err := io.WriteString(w, "data: "+string(b)+"\n\n"); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(current) {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		case b := <-ch:
			if !send(b) {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func (s *server) icon(w http.ResponseWriter, r *http.Request) {
	data, ctype, err := s.icons.get(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		w.Header().Set("Cache-Control", "max-age=300")
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "public, max-age=604800")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Write(data)
}

func main() {
	configPath := envOr("CONFIG_FILE", "/config/tiles.yaml")
	cacheDir := envOr("CACHE_DIR", "/cache")
	addr := ":" + envOr("PORT", "8080")

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Printf("icon cache disabled, cannot use %s: %v", cacheDir, err)
	}
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatal(err)
	}

	cfg := newConfig(configPath)
	s := &server{cfg: cfg, icons: newIconCache(cacheDir), page: string(page), quit: make(chan struct{})}

	assets, _ := fs.Sub(webFS, "web/assets")
	assetHandler := http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/board", s.board)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/icon", s.icon)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.Handle("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		assetHandler.ServeHTTP(w, r)
	}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stopWatch := context.WithCancel(context.Background())
	go cfg.watch(ctx, 2*time.Second)
	go newNetworkWatcher(cfg).run(ctx)
	go newServiceWatcher(cfg).run(ctx)
	go newDiscoveryWatcher(cfg).run(ctx)

	go func() {
		log.Printf("view listening on %s, reading tiles from %s", addr, configPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopWatch()
	close(s.quit)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

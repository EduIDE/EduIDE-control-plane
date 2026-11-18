// Command server serves the built landing page, its runtime config and a health check.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

type config struct {
	Keycloak struct {
		URL string `json:"url"`
	} `json:"keycloak"`
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dist := flag.String("dist", "/app/dist", "directory with the built app")
	cfgPath := flag.String("config", "/etc/eduide/landing/config.json", "runtime config served as /config.json")
	flag.Parse()

	h, err := newHandler(*dist, *cfgPath)
	if err != nil {
		slog.Error("start", "error", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

// newHandler reads the config once to derive the CSP and serves the file again on every request,
// so a ConfigMap update shows up without a restart (the CSP origin needs one).
func newHandler(dist, cfgPath string) (http.Handler, error) {
	raw, err := os.ReadFile(cfgPath) //nolint:gosec // operator-provided flag
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	csp, err := contentSecurityPolicy(cfg.Keycloak.URL)
	if err != nil {
		return nil, err
	}
	root := os.DirFS(dist)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /config.json", func(w http.ResponseWriter, _ *http.Request) {
		b, err := os.ReadFile(cfgPath) //nolint:gosec // operator-provided flag
		if err != nil {
			http.Error(w, "config unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		f, err := root.Open(name)
		if err == nil {
			if st, serr := f.Stat(); serr != nil || st.IsDir() {
				_ = f.Close()
				err = errors.New("not a file")
			}
		}
		if err != nil {
			// SPA fallback: unknown paths belong to the client router, but a missing asset stays a 404.
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
			if f, err = root.Open(name); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		defer func() { _ = f.Close() }()
		st, _ := f.Stat()
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		rs, ok := f.(interface {
			Read([]byte) (int, error)
			Seek(int64, int) (int64, error)
		})
		if !ok {
			http.Error(w, "unsupported file", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, st.ModTime(), rs)
	})
	return securityHeaders(csp, mux), nil
}

func securityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func contentSecurityPolicy(keycloakURL string) (string, error) {
	connect := "'self'"
	if keycloakURL != "" {
		u, err := url.Parse(keycloakURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("keycloak.url %q is not an absolute URL", keycloakURL)
		}
		connect += " " + u.Scheme + "://" + u.Host
	}
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src " + connect + "; form-action 'self'; base-uri 'self'; frame-ancestors 'none'", nil
}

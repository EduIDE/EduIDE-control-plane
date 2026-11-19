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
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

type config struct {
	Keycloak struct {
		URL string `json:"url"`
	} `json:"keycloak"`
	// HSTSIncludeSubdomains defaults to true; the landing host is the parent of the session hosts.
	HSTSIncludeSubdomains *bool `json:"hstsIncludeSubdomains"`
}

// logger writes JSON on stdout like the other binaries; LOG_LEVEL is debug, info or warn.
func logger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", "eduide-landing-page")
}

// version is the VCS revision of the build.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(12, len(s.Value))]
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return bi.Main.Version
	}
	return rev + dirty
}

func main() {
	slog.SetDefault(logger())
	addr := flag.String("addr", ":8080", "listen address")
	dist := flag.String("dist", "/app/dist", "directory with the built app")
	cfgPath := flag.String("config", "/etc/eduide/landing/config.json", "runtime config served as /config.json")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, *dist, *cfgPath); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

// run serves until ctx ends and logs start, ready and shutdown.
func run(ctx context.Context, addr, dist, cfgPath string) error {
	h, err := newHandler(dist, cfgPath)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		slog.Info("shutdown", "reason", "signal")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("starting", "version", version(), "addr", addr, "dist", dist, "config", cfgPath, "log_level", os.Getenv("LOG_LEVEL"))
	slog.Info("ready", "detail", "config read, serving")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
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
	hsts := "max-age=31536000; includeSubDomains"
	if cfg.HSTSIncludeSubdomains != nil && !*cfg.HSTSIncludeSubdomains {
		hsts = "max-age=31536000"
	}
	return securityHeaders(csp, hsts, mux), nil
}

func securityHeaders(csp, hsts string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("request", "method", r.Method, "path", r.URL.Path)
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Strict-Transport-Security", hsts)
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

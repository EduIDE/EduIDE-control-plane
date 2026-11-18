package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T, cfg string, extra ...string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "INDEX", "assets/a.js": "JS"} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := newHandler(dist, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandler(t *testing.T) {
	h := setup(t, `{"keycloak":{"url":"https://kc.example.org/auth"}}`)
	tests := []struct {
		path     string
		code     int
		body     string
		cacheHas string
	}{
		{"/", 200, "INDEX", "no-cache"},
		{"/me/overview", 200, "INDEX", "no-cache"},
		{"/assets/a.js", 200, "JS", "immutable"},
		{"/assets/missing.js", 404, "", ""},
		{"/config.json", 200, `{"keycloak"`, "no-store"},
		{"/healthz", 200, "ok", ""},
		{"/../../etc/passwd", 307, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d", rec.Code, tc.code)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body %q lacks %q", rec.Body.String(), tc.body)
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), tc.cacheHas) {
				t.Errorf("cache-control %q lacks %q", rec.Header().Get("Cache-Control"), tc.cacheHas)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Error("security headers missing")
			}
			if rec.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
				t.Error("HSTS missing")
			}
			if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "connect-src 'self' https://kc.example.org;") {
				t.Errorf("CSP lacks keycloak origin: %s", rec.Header().Get("Content-Security-Policy"))
			}
		})
	}
}

func TestBadConfig(t *testing.T) {
	if _, err := newHandler(t.TempDir(), filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("missing config must fail")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	_ = os.WriteFile(p, []byte(`{"keycloak":{"url":"not a url"}}`), 0o644)
	if _, err := newHandler(dir, p); err == nil {
		t.Error("relative keycloak url must fail")
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func runLogged(t *testing.T, level slog.Level) string {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})))
	defer slog.SetDefault(old)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("INDEX"), 0o644)
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"keycloak":{"url":"https://kc-secret.example.org"}}`), 0o644)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, dir, cfg) }()
	for range 100 {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestStartReadyShutdownLines(t *testing.T) {
	out := runLogged(t, slog.LevelInfo)
	for _, want := range []string{`"msg":"starting"`, `"version":`, `"msg":"ready"`, `"msg":"shutdown"`, `"reason":"signal"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"level":"DEBUG"`) || strings.Contains(out, "kc-secret") {
		t.Errorf("unexpected content:\n%s", out)
	}
}

func TestRequestsAreLoggedAtDebugOnly(t *testing.T) {
	if out := runLogged(t, slog.LevelDebug); !strings.Contains(out, `"msg":"request"`) {
		t.Errorf("no request line at debug:\n%s", out)
	}
}

func TestLegalPages(t *testing.T) {
	h := setup(t, `{"keycloak":{"url":"https://kc.example.org"}}`, "imprint.md")
	tests := []struct {
		path string
		code int
		body string
	}{
		{"/legal/imprint.md", 200, "# imprint.md"},
		{"/legal/privacy.md", 404, ""},
		{"/legal/config.json", 404, ""},
		{"/legal/other.md", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d", rec.Code, tc.code)
			}
			if tc.code != 200 {
				return
			}
			if rec.Body.String() != tc.body {
				t.Errorf("body %q, want %q", rec.Body.String(), tc.body)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
				t.Errorf("content-type %q", ct)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("cache-control %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

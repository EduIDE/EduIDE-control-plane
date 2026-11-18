package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, cfg string) http.Handler {
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

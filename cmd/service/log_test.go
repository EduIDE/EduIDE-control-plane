package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const testConfig = `
deployment: test
namespace: eduide-test
hosts: {service: ide.example.org, instance: instance.ide.example.org}
gateway: {name: gw}
oidc: {issuerURL: https://kc.example.org/realms/r, clientIDs: [landing-client-id], gatewayClientID: gw-client-id}
agent: {image: example.org/agent:1}
telemetry: {otlpEndpoint: otel:4317}
`

func TestStartingLine(t *testing.T) {
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	logStarting(cfg, ":8080", ":9090", []byte("salt-secret-bytes"))
	out := buf.String()
	for _, want := range []string{`"msg":"starting"`, `"version":`, `"deployment":"test"`, `"namespace":"eduide-test"`, `"otlp_endpoint_set":true`, `"user_salt_set":true`, `"log_level":`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"salt-secret-bytes", "otel:4317", "gw-client-id", "landing-client-id"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
}

// SSE streams need a server without write and read timeouts; everything else must be bounded.
func TestHTTPServersAreBounded(t *testing.T) {
	for _, s := range httpServers(t.Context(), http.NotFoundHandler(), http.NotFoundHandler()) {
		if s.ReadHeaderTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
			t.Errorf("%s: header timeout %s, idle timeout %s, header bytes %d", s.Addr, s.ReadHeaderTimeout, s.IdleTimeout, s.MaxHeaderBytes)
		}
		if s.WriteTimeout != 0 || s.ReadTimeout != 0 {
			t.Errorf("%s: a write or read timeout would cut event streams", s.Addr)
		}
	}
}

package main

import (
	"bytes"
	"log/slog"
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
	logStarting(cfg, ":9090", []byte("salt-secret-bytes"))
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

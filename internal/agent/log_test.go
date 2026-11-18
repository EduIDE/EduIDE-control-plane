package agent

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const claimJSON = `{"session":"s-1","data":{"GIT_URI":"https://git.example.org/launch-data-value","ARTEMIS_TOKEN":"artemis-secret-value"}}`

// exercise drives every logging path of the agent against an IDE at ideURL and returns the log.
func exercise(t *testing.T, level slog.Level, ideURL string) string {
	t.Helper()
	var buf bytes.Buffer
	a := New(tok, portOf(t, ideURL), slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
	proxy, ctl, local := a.ProxyHandler(), a.ControlHandler(), a.LocalHandler()
	do(t, ctl, "POST", "/v1/claim", claimJSON, bearer)
	do(t, ctl, "POST", "/v1/claim", claimJSON, bearer) // re-claim
	do(t, ctl, "POST", "/v1/claim", `{"session":"other"}`, bearer)
	do(t, ctl, "POST", "/v1/notify", `{"kind":"idle-warning","message":"notice-text-value"}`, bearer)
	do(t, ctl, "POST", "/v1/claim", claimJSON, map[string]string{"Authorization": "Bearer wrong-token-value"})
	do(t, proxy, "GET", "/ide-file-name/x", "", map[string]string{TokenHeader: "wrong-proxy-token"})
	do(t, local, "POST", "/v1/heartbeat", "", nil)
	do(t, ctl, "GET", "/readyz", "", nil)
	do(t, proxy, "GET", "/folder/user-file-name", "", map[string]string{TokenHeader: tok, "Cookie": "eduide-id=cookie-value"})
	return buf.String()
}

func TestAgentLogs(t *testing.T) {
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ide.Close()
	out := exercise(t, slog.LevelInfo, ide.URL)
	for _, want := range []string{
		`"msg":"claimed"`, `"keys":["ARTEMIS_TOKEN","GIT_URI"]`, `"msg":"re-claimed"`, `"msg":"claim refused`,
		`"msg":"notice queued"`, `"kind":"idle-warning"`, `"msg":"token check failed"`, `"endpoint":"control"`, `"endpoint":"proxy"`,
		`"msg":"ide port reachable"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"level":"DEBUG"`) {
		t.Errorf("debug line at info level:\n%s", out)
	}
	assertNoSecrets(t, out)
}

func TestAgentDebugLines(t *testing.T) {
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ide.Close()
	out := exercise(t, slog.LevelDebug, ide.URL)
	for _, want := range []string{`"msg":"proxy request"`, `"prefix":"/folder"`, `"status":200`, `"msg":"heartbeat"`, `"msg":"readiness probe"`} {
		if !strings.Contains(out, want) {
			t.Errorf("debug log lacks %s:\n%s", want, out)
		}
	}
	assertNoSecrets(t, out)
}

func TestAgentLogsPortStateChangesAndUpstreamErrors(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := "http://" + l.Addr().String()
	_ = l.Close() // nothing listens: unreachable and a bad gateway
	out := exercise(t, slog.LevelInfo, addr)
	for _, want := range []string{`"msg":"ide port unreachable"`, `"msg":"proxy upstream error"`, `"level":"WARN"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	assertNoSecrets(t, out)
}

func assertNoSecrets(t *testing.T, out string) {
	t.Helper()
	for _, bad := range []string{"launch-data-value", "artemis-secret-value", "wrong-token-value", "wrong-proxy-token", tok, "cookie-value", "notice-text-value", "user-file-name", "ide-file-name"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
}

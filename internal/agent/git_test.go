package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func gitAgent(t *testing.T, proxyURL string, claimed bool) *Agent {
	t.Helper()
	a := newAgent(t, "3000")
	a.PathPrefix = "/inst1/"
	if proxyURL != "" {
		if err := a.EnableGit(proxyURL, []string{"github.com", "gitlab.example.org"}, "https://ide.example.org"); err != nil {
			t.Fatal(err)
		}
	}
	if claimed {
		do(t, a.ControlHandler(), "POST", "/v1/claim", `{"session":"s1","data":{}}`, map[string]string{"Authorization": "Bearer " + tok})
	}
	return a
}

func TestGitFeatureOff(t *testing.T) {
	a := gitAgent(t, "", true)
	if rec := do(t, a.LocalHandler(), "GET", "/git/github.com/o/r/info/refs?service=git-upload-pack", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/git/ with the feature off: %d", rec.Code)
	}
	rec := do(t, a.LocalHandler(), "GET", "/v1/git/hosts", "", nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hosts":[]}` {
		t.Errorf("hosts with the feature off: %d %s", rec.Code, rec.Body)
	}
}

func TestGitHosts(t *testing.T) {
	a := gitAgent(t, "http://127.0.0.1:1", false)
	rec := do(t, a.LocalHandler(), "GET", "/v1/git/hosts", "", nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hosts":["github.com","gitlab.example.org"],"landingUrl":"https://ide.example.org"}` {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestGitRouteClaimedAndIdle(t *testing.T) {
	var got *http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("WWW-Authenticate", "Basic")
		w.Header().Set("Set-Cookie", "a=b")
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	path := "/git/github.com/o/r.git/info/refs?service=git-upload-pack"
	hdr := map[string]string{
		"Authorization": "Basic Zm9vOmJhcg==", "Cookie": "eduide-id=x; a=b", "Proxy-Authorization": "Basic eA==",
		gitapi.InstanceHeader: "other-instance", TokenHeader: "x", "Git-Protocol": "version=2",
	}

	idle := gitAgent(t, up.URL, false)
	if rec := do(t, idle.LocalHandler(), "GET", path, "", hdr); rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("idle instance: %d, upstream called: %v", rec.Code, got != nil)
	}

	a := gitAgent(t, up.URL, true)
	rec := do(t, a.LocalHandler(), "GET", path, "", hdr)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("claimed: %d %s", rec.Code, rec.Body)
	}
	if got.URL.Path != "/github.com/o/r.git/info/refs" || got.URL.RawQuery != "service=git-upload-pack" {
		t.Errorf("forwarded as %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer "+tok || got.Header.Get(gitapi.InstanceHeader) != "inst1" {
		t.Errorf("instance credentials: %v", got.Header)
	}
	for _, h := range []string{"Cookie", "Proxy-Authorization", TokenHeader} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s reached the proxy", h)
		}
	}
	if got.Header.Get("Git-Protocol") != "version=2" {
		t.Error("Git-Protocol was dropped")
	}
	if rec.Header().Get("WWW-Authenticate") != "" || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("response headers reach the IDE: %v", rec.Header())
	}
}

func TestEnableGitValidates(t *testing.T) {
	a := newAgent(t, "3000")
	if err := a.EnableGit("http://x:1", nil, ""); err == nil {
		t.Error("no instance name")
	}
	a.PathPrefix = "/i/"
	for _, u := range []string{"", "ftp://x", "http://", "not a url"} {
		if err := a.EnableGit(u, nil, ""); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

package feedback

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGitHub struct {
	srv        *httptest.Server
	tokenCalls atomic.Int32
	issues     chan map[string]any
	jwt        string
	issueAuth  string
	issueCode  int
	tokenExp   func() time.Time
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{issues: make(chan map[string]any, 10), issueCode: http.StatusCreated, tokenExp: func() time.Time { return time.Now().Add(time.Hour) }}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		f.jwt = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "inst-token", "expires_at": f.tokenExp()})
	})
	mux.HandleFunc("POST /repos/EduIDE/feedback-inbox/issues", func(w http.ResponseWriter, r *http.Request) {
		f.issueAuth = r.Header.Get("Authorization")
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.issues <- m
		w.WriteHeader(f.issueCode)
		_, _ = w.Write([]byte(`{"number":42,"secret":"x"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T) (*Server, *fakeGitHub, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeGitHub(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bonn"), []byte("secret-bonn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stud"), []byte("secret-stud"), 0o600); err != nil {
		t.Fatal(err)
	}
	gh := &GitHub{APIURL: f.srv.URL, AppID: "123", InstallationID: "7", Repository: "EduIDE/feedback-inbox", Key: key}
	return &Server{KeysDir: dir, GitHub: gh}, f, key
}

func body(m map[string]any) *bytes.Reader {
	base := map[string]any{"kind": "bug", "title": "Broken", "description": "It breaks", "installation": "bonn", "host": "ide.example.org",
		"context": map[string]any{"version": "3.0.0", "appDefinition": "java", "browser": "Firefox"}}
	for k, v := range m {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, _ := json.Marshal(base)
	return bytes.NewReader(b)
}

func post(s *Server, auth string, b *bytes.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/feedback", b)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAuth(t *testing.T) {
	s, f, _ := setup(t)
	for name, tc := range map[string]struct {
		auth string
		in   map[string]any
	}{
		"no header":            {"", nil},
		"wrong key":            {"Bearer nope", nil},
		"key of another":       {"Bearer secret-stud", nil},
		"unknown installation": {"Bearer secret-bonn", map[string]any{"installation": "other"}},
		"traversal":            {"Bearer x", map[string]any{"installation": "../bonn"}},
		"empty installation":   {"Bearer ", map[string]any{"installation": ""}},
	} {
		if w := post(s, tc.auth, body(tc.in)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, w.Code)
		}
	}
	if len(f.issues) != 0 {
		t.Error("an issue was created without auth")
	}
}

func TestValidation(t *testing.T) {
	s, _, _ := setup(t)
	for name, in := range map[string]map[string]any{
		"kind":         {"kind": "praise"},
		"empty title":  {"title": ""},
		"long title":   {"title": strings.Repeat("a", 121)},
		"blank desc":   {"description": "   "},
		"long desc":    {"description": strings.Repeat("a", 5001)},
		"long context": {"context": map[string]any{"browser": strings.Repeat("a", 301)}},
	} {
		if w := post(s, "Bearer secret-bonn", body(in)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, w.Code)
		}
	}
	// Unknown fields, broken JSON and bodies over 32 KiB are refused.
	for name, b := range map[string]*bytes.Reader{
		"extra field": bytes.NewReader([]byte(`{"kind":"bug","title":"t","description":"d","installation":"bonn","user":"me"}`)),
		"not json":    bytes.NewReader([]byte(`nope`)),
		"too large":   body(map[string]any{"description": strings.Repeat("a", 40<<10)}),
	} {
		if w := post(s, "Bearer secret-bonn", b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, w.Code)
		}
	}
}

func TestIssuePayload(t *testing.T) {
	s, f, _ := setup(t)
	w := post(s, "Bearer secret-bonn", body(map[string]any{"title": "Line one\nline|two", "kind": "feature",
		"context": map[string]any{"version": "3.0.0", "appDefinition": "a|b", "browser": "Firefox"}}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "42") {
		t.Error("response leaks the issue")
	}
	got := <-f.issues
	if got["title"] != "[bonn] Line one line|two" {
		t.Errorf("title %q", got["title"])
	}
	labels, _ := json.Marshal(got["labels"])
	if string(labels) != `["via-ide","needs-triage","enhancement"]` {
		t.Errorf("labels %s", labels)
	}
	if len(got) != 3 {
		t.Errorf("extra fields in payload: %v", got)
	}
	b := got["body"].(string)
	for _, want := range []string{"```text\nIt breaks\n```", "| installation | bonn |", "| host | ide.example.org |", "| version | 3.0.0 |", `| app definition | a\|b |`, "| browser | Firefox |", "| kind | feature |"} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %q:\n%s", want, b)
		}
	}
	if f.issueAuth != "Bearer inst-token" {
		t.Errorf("issue auth %q", f.issueAuth)
	}
	post(s, "Bearer secret-bonn", body(nil))
	if l, _ := json.Marshal((<-f.issues)["labels"]); string(l) != `["via-ide","needs-triage","bug"]` {
		t.Errorf("bug labels %s", l)
	}
}

func TestRateLimitPerInstallation(t *testing.T) {
	s, _, _ := setup(t)
	s.Limit = 2
	for i := 0; i < 2; i++ {
		if w := post(s, "Bearer secret-bonn", body(nil)); w.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := post(s, "Bearer secret-bonn", body(nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Errorf("got %d, retry-after %q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := post(s, "Bearer secret-stud", body(map[string]any{"installation": "stud"})); w.Code != http.StatusAccepted {
		t.Errorf("other installation is limited too: %d", w.Code)
	}
}

func TestGitHubFailureIs502(t *testing.T) {
	s, f, _ := setup(t)
	f.issueCode = http.StatusInternalServerError
	if w := post(s, "Bearer secret-bonn", body(nil)); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "500") {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
	s.GitHub.(*GitHub).APIURL = "http://127.0.0.1:1"
	if w := post(s, "Bearer secret-bonn", body(nil)); w.Code != http.StatusBadGateway {
		t.Errorf("unreachable GitHub: %d", w.Code)
	}
}

func TestJWTClaimsAndSignature(t *testing.T) {
	s, f, key := setup(t)
	now := time.Unix(1_800_000_000, 0)
	s.GitHub.(*GitHub).Now = func() time.Time { return now }
	f.tokenExp = func() time.Time { return now.Add(time.Hour) }
	if w := post(s, "Bearer secret-bonn", body(nil)); w.Code != http.StatusAccepted {
		t.Fatal(w.Code)
	}
	parts := strings.Split(f.jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", f.jwt)
	}
	dec := func(p string) []byte { b, err := base64.RawURLEncoding.DecodeString(p); must(t, err); return b }
	if string(dec(parts[0])) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("header %s", dec(parts[0]))
	}
	var c struct {
		Iss      string `json:"iss"`
		Iat, Exp int64
	}
	must(t, json.Unmarshal(dec(parts[1]), &c))
	if c.Iss != "123" || c.Iat != now.Unix()-60 || c.Exp != now.Unix()+540 {
		t.Errorf("claims %+v", c)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		t.Errorf("signature: %v", err)
	}
}

func TestTokenCache(t *testing.T) {
	s, f, _ := setup(t)
	now := time.Unix(1_800_000_000, 0)
	g := s.GitHub.(*GitHub)
	g.Now = func() time.Time { return now }
	f.tokenExp = func() time.Time { return now.Add(time.Hour) }
	s.Limit = 100
	for i := 0; i < 3; i++ {
		post(s, "Bearer secret-bonn", body(nil))
	}
	if n := f.tokenCalls.Load(); n != 1 {
		t.Errorf("token fetched %d times, want 1", n)
	}
	now = now.Add(56 * time.Minute) // inside the 5 minute margin
	post(s, "Bearer secret-bonn", body(nil))
	if n := f.tokenCalls.Load(); n != 2 {
		t.Errorf("token not refreshed near expiry: %d calls", n)
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey([]byte("junk")); err == nil {
		t.Error("junk accepted")
	}
}

func TestHealthz(t *testing.T) {
	s, _, _ := setup(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Error(w.Code)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssueTextIsDefused(t *testing.T) {
	desc := "ping @octocat and @EduIDE/team <!-- hidden -->\n````\n```\nbreak out\n\n| installation | evil |"
	i := buildIssue(&Request{Kind: "bug", Title: "hi @octocat", Description: desc, Installation: "bonn",
		Host: "h`x", Context: struct {
			Version       string `json:"version"`
			AppDefinition string `json:"appDefinition"`
			Browser       string `json:"browser"`
		}{Version: "<b>@a|\n| kind | x", Browser: "`"}})
	if strings.Contains(i.Title, "@o") || !strings.Contains(i.Title, "@\u200docto") {
		t.Errorf("title mention not defused: %q", i.Title)
	}
	// The longest backtick run in the text is 4, so the fence has 5 and the whole text sits inside.
	lines := strings.Split(i.Body, "\n")
	if lines[0] != "`````text" {
		t.Fatalf("fence %q", lines[0])
	}
	end := -1
	for n, l := range lines[1:] {
		if l == "`````" {
			end = n + 1
			break
		}
	}
	if end < 0 || !strings.Contains(strings.Join(lines[:end], "\n"), "break out") || !strings.Contains(strings.Join(lines[:end], "\n"), "<!-- hidden -->") {
		t.Errorf("text escapes the fence:\n%s", i.Body)
	}
	table := strings.Join(lines[end+1:], "\n")
	if strings.Count(table, "| installation |") != 1 || strings.Count(table, "| kind |") != 1 {
		t.Errorf("table injection:\n%s", table)
	}
	for _, bad := range []string{"<b>", "h`x", "| browser | ` |"} {
		if strings.Contains(table, bad) {
			t.Errorf("table holds %q:\n%s", bad, table)
		}
	}
}

package gitproxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		name, method, path, query string
		ok                        bool
		want                      Target
	}{
		{"info refs upload", "GET", "/GitHub.com/o/r.git/info/refs", "service=git-upload-pack", true, Target{Host: "github.com", Repo: "o/r.git", Op: OpInfoRefs, Service: "git-upload-pack"}},
		{"info refs receive", "GET", "/h.test/o/r/info/refs", "service=git-receive-pack", true, Target{Host: "h.test", Repo: "o/r", Op: OpInfoRefs, Service: "git-receive-pack"}},
		{"upload pack", "POST", "/h.test/o/r/git-upload-pack", "", true, Target{Host: "h.test", Repo: "o/r", Op: OpUploadPack, Service: "git-upload-pack"}},
		{"receive pack", "POST", "/h.test/g/sub/r.git/git-receive-pack", "", true, Target{Host: "h.test", Repo: "g/sub/r.git", Op: OpReceivePack, Service: "git-receive-pack"}},
		{"lfs batch", "POST", "/h.test/o/r.git/info/lfs/objects/batch", "", true, Target{Host: "h.test", Repo: "o/r.git", Op: OpLFS}},
		{"dumb protocol", "GET", "/h.test/o/r/info/refs", "", false, Target{}},
		{"unknown service", "GET", "/h.test/o/r/info/refs", "service=other", false, Target{}},
		{"two services", "GET", "/h.test/o/r/info/refs", "service=git-upload-pack&service=git-receive-pack", false, Target{}},
		{"wrong method info refs", "POST", "/h.test/o/r/info/refs", "service=git-upload-pack", false, Target{}},
		{"wrong method pack", "GET", "/h.test/o/r/git-upload-pack", "", false, Target{}},
		{"HEAD file", "GET", "/h.test/o/r/HEAD", "", false, Target{}},
		{"objects", "GET", "/h.test/o/r/objects/info/packs", "", false, Target{}},
		{"api path", "GET", "/h.test/api/v3/user", "", false, Target{}},
		{"dot dot", "GET", "/h.test/o/../x/info/refs", "service=git-upload-pack", false, Target{}},
		{"dot", "GET", "/h.test/o/./r/info/refs", "service=git-upload-pack", false, Target{}},
		{"empty segment", "GET", "/h.test/o//info/refs", "service=git-upload-pack", false, Target{}},
		{"one segment repo", "GET", "/h.test/r/info/refs", "service=git-upload-pack", false, Target{}},
		{"bad character", "GET", "/h.test/o/r@x/info/refs", "service=git-upload-pack", false, Target{}},
		{"percent", "GET", "/h.test/o/r%2Fx/info/refs", "service=git-upload-pack", false, Target{}},
		{"no host", "GET", "//o/r/info/refs", "service=git-upload-pack", false, Target{}},
		{"too deep", "GET", "/h.test/" + strings.Repeat("a/", 11) + "info/refs", "service=git-upload-pack", false, Target{}},
		{"root", "GET", "/", "", false, Target{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePath(tc.method, tc.path, tc.query)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("got %+v %v, want %+v %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestKind(t *testing.T) {
	if (Target{Service: "git-receive-pack"}).Kind() != gitapi.KindPush || (Target{Service: "git-upload-pack"}).Kind() != gitapi.KindRead {
		t.Error("kind")
	}
}

func TestDecisionCache(t *testing.T) {
	c := newDecisionCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	key := func(token string) cacheKey {
		return cacheKey{instance: "i", host: "h", repo: "r", kind: "read", token: [32]byte{token[0]}}
	}
	ok := gitapi.AuthorizeResponse{Allow: true, Reason: gitapi.ReasonOK}
	anon := gitapi.AuthorizeResponse{Allow: true, Reason: gitapi.ReasonAnonymous}
	denied := gitapi.AuthorizeResponse{Reason: gitapi.ReasonPushRule}
	c.put(key("a"), ok)
	c.put(key("b"), anon)
	c.put(key("c"), denied)
	for _, k := range []string{"a", "b", "c"} {
		if _, hit := c.get(key(k)); !hit {
			t.Errorf("miss %s", k)
		}
	}
	if _, hit := c.get(key("d")); hit {
		t.Error("another token hits")
	}
	now = now.Add(shortTTL + time.Second)
	if _, hit := c.get(key("a")); !hit {
		t.Error("a credential answer is kept for up to 5s")
	}
	if _, hit := c.get(key("b")); hit {
		t.Error("an anonymous answer outlives the short TTL")
	}
	if _, hit := c.get(key("c")); hit {
		t.Error("a denial outlives the short TTL")
	}
	now = now.Add(allowTTL)
	if _, hit := c.get(key("a")); hit {
		t.Error("an answer outlives 5s")
	}
}

func TestDecisionCacheIsBounded(t *testing.T) {
	c := newDecisionCache()
	for i := range maxCacheSize + 100 {
		c.put(cacheKey{instance: string(rune('a' + i%26)), repo: strings.Repeat("x", i)}, gitapi.AuthorizeResponse{})
	}
	if len(c.m) > maxCacheSize {
		t.Errorf("%d entries", len(c.m))
	}
}

type azFunc func(ctx context.Context, token string, req gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error)

func (f azFunc) Authorize(ctx context.Context, token string, req gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
	return f(ctx, token, req)
}

func newTestHandler(az Authorizer) *Handler {
	return New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", az, nil, slog.New(slog.DiscardHandler))
}

func TestWithoutInstanceOrTokenNothingGoesUpstream(t *testing.T) {
	called := false
	h := newTestHandler(azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		called = true
		return gitapi.AuthorizeResponse{Allow: true}, nil
	}))
	for name, hdr := range map[string]http.Header{
		"nothing":       {},
		"token only":    {"Authorization": {"Bearer t"}},
		"instance only": {gitapi.InstanceHeader: {"i"}},
		"basic instead": {gitapi.InstanceHeader: {"i"}, "Authorization": {"Basic dDp0"}},
		"empty bearer":  {gitapi.InstanceHeader: {"i"}, "Authorization": {"Bearer "}},
	} {
		req := httptest.NewRequest("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil)
		req.Header = hdr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
		}
	}
	if called {
		t.Error("the service was asked without credentials")
	}
}

func TestUnknownHostAndPathAre404(t *testing.T) {
	h := newTestHandler(azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		t.Error("asked")
		return gitapi.AuthorizeResponse{}, nil
	}))
	for _, p := range []string{"/other.test/o/r/info/refs?service=git-upload-pack", "/h.test/o/r/HEAD", "/", "/h.test/api/user"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestAuthorizeFailureIsAReadableGitError(t *testing.T) {
	h := newTestHandler(azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{}, errors.New("down")
	}))
	req := httptest.NewRequest("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil)
	req.Header.Set(gitapi.InstanceHeader, "i")
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ERR EduIDE cannot check your access right now") ||
		rec.Header().Get("Content-Type") != "application/x-git-upload-pack-advertisement" {
		t.Errorf("%d %q %v", rec.Code, rec.Body, rec.Header())
	}
}

func TestAnswersAreCachedPerTokenAndRepository(t *testing.T) {
	calls := 0
	h := newTestHandler(azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		calls++
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonPushRule, Message: "no"}, nil
	}))
	do := func(token, path string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set(gitapi.InstanceHeader, "i")
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	do("t1", "/h.test/o/r/info/refs?service=git-receive-pack")
	do("t1", "/h.test/o/r.git/info/refs?service=git-receive-pack") // same repository after normalisation
	do("t2", "/h.test/o/r/info/refs?service=git-receive-pack")
	do("t1", "/h.test/o/x/info/refs?service=git-receive-pack")
	if calls != 3 {
		t.Errorf("%d calls, want 3", calls)
	}
}

func TestDefaultTransportVerifiesTLSAndIgnoresProxyEnv(t *testing.T) {
	tr := newTestHandler(nil).rp.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("TLS verification is off")
	}
	if tr.Proxy != nil {
		t.Error("the transport uses a proxy from the environment")
	}
}

func TestServiceClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			_, _ = w.Write([]byte(`{"allow":true,"reason":"ok","upstream":{"username":"u","password":"p"}}`))
		case "Bearer bad":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	c := &ServiceClient{URL: srv.URL, HTTP: srv.Client()}
	req := gitapi.AuthorizeRequest{Instance: "i", Host: "h", Repository: "r", Kind: "read"}
	if r, err := c.Authorize(context.Background(), "good", req); err != nil || !r.Allow || r.Upstream.Password != "p" {
		t.Errorf("good: %+v %v", r, err)
	}
	if r, err := c.Authorize(context.Background(), "bad", req); err != nil || r.Allow || r.Reason != gitapi.ReasonUnauthorized {
		t.Errorf("bad: %+v %v", r, err)
	}
	if _, err := c.Authorize(context.Background(), "other", req); err == nil {
		t.Error("429 is an error")
	}
}

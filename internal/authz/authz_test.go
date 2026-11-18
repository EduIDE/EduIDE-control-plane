package authz

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	clientID  = "gateway"
	instHost  = "ide.example.org"
	idCookie  = "eduide-id"
	instanceA = "aaaaaaaaaaaaaaaaaaaa"
)

type issuer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	key  *rsa.PrivateKey
	hits atomic.Int32 // JWKS requests
}

func (i *issuer) signingKey() *rsa.PrivateKey {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.key
}

func (i *issuer) rotate(t testing.TB) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	i.key = key
	i.mu.Unlock()
}

func newIssuer(t testing.TB) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &issuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": i.srv.URL, "jwks_uri": i.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		i.hits.Add(1)
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &i.signingKey().PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

// token signs an ID token; zero values of the optional arguments mean "valid".
func (i *issuer) token(t testing.TB, key *rsa.PrivateKey, iss, aud, user string, exp time.Duration) string {
	t.Helper()
	if key == nil {
		key = i.signingKey()
	}
	if iss == "" {
		iss = i.srv.URL
	}
	if aud == "" {
		aud = clientID
	}
	if exp == 0 {
		exp = time.Hour
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"preferred_username": user}
	s, err := jwt.Signed(sig).Claims(jwt.Claims{Issuer: iss, Audience: jwt.Audience{aud}, Subject: "sub",
		Expiry: jwt.NewNumericDate(time.Now().Add(exp)), IssuedAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))}).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type fakeSessions struct {
	by  map[string][]v1alpha1.IDESession
	err error
}

func (f fakeSessions) ByInstance(_ context.Context, i string) ([]v1alpha1.IDESession, error) {
	return f.by[i], f.err
}

func session(owner, instance string, phase v1alpha1.SessionPhase, deleting bool) v1alpha1.IDESession {
	s := v1alpha1.IDESession{
		Spec:   v1alpha1.IDESessionSpec{Owner: owner},
		Status: v1alpha1.IDESessionStatus{Instance: instance, Phase: phase},
	}
	if deleting {
		s.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	}
	return s
}

func newServer(t testing.TB, iss *issuer, sessions Sessions) *Server {
	t.Helper()
	p, err := oidc.NewProvider(context.Background(), iss.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Sessions: sessions, UserClaim: "preferred_username", IDTokenCookie: idCookie,
		InstanceHost: instHost, WebviewHost: "webview." + instHost, Salt: []byte("salt")}
	v, err := Verifier(p, clientID, http.DefaultClient, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s.SetVerifier(v)
	return s
}

func req(path, cookie string) *authv3.CheckRequest {
	h := &authv3.AttributeContext_HttpRequest{Path: path, Host: instHost, Headers: map[string]string{}}
	if cookie != "" {
		h.Headers["cookie"] = cookie
	}
	return &authv3.CheckRequest{Attributes: &authv3.AttributeContext{Request: &authv3.AttributeContext_Request{Http: h}}}
}

func TestDecisionTable(t *testing.T) {
	iss := newIssuer(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	sessions := fakeSessions{by: map[string][]v1alpha1.IDESession{
		instanceA:              {session("alice", instanceA, v1alpha1.SessionReady, false)},
		"bbbbbbbbbbbbbbbbbbbb": {session("alice", "bbbbbbbbbbbbbbbbbbbb", v1alpha1.SessionStarting, false)},
		"cccccccccccccccccccc": {session("alice", "cccccccccccccccccccc", v1alpha1.SessionReady, true)},
	}}
	s := newServer(t, iss, sessions)
	good := idCookie + "=" + iss.token(t, nil, "", "", "alice", 0)
	tests := []struct {
		name, path, cookie string
		result, reason     string
		http               typev3.StatusCode
	}{
		{"oauth2 path without cookies", "/oauth2/callback?code=x", "", "allow", "oauth2", 0},
		{"oauth2 path with bad token", "/oauth2/callback", idCookie + "=junk", "allow", "oauth2", 0},
		{"oauth2 path traversal", "/oauth2/../" + instanceA + "/", good + "x", "deny", "bad-path", typev3.StatusCode_Forbidden},
		// With escapedSlashesAction KeepUnchanged Envoy routes these to the first literal segment.
		{"encoded traversal into oauth2", "/bbbbbbbbbbbbbbbbbbbb/..%2Foauth2/x", idCookie + "=" + iss.token(t, nil, "", "", "mallory", 0), "deny", "bad-path", typev3.StatusCode_Forbidden},
		{"encoded traversal into own instance", "/zzzzzzzzzzzzzzzzzzzz/..%2F" + instanceA + "/", good, "deny", "bad-path", typev3.StatusCode_Forbidden},
		{"AccessToken only, no id token", "/" + instanceA + "/", "AccessToken-abc=x", "login", "invalid-token", typev3.StatusCode_Found},
		{"no cookies", "/" + instanceA + "/", "", "allow", "no-cookie", 0},
		{"unrelated cookies", "/" + instanceA + "/", "a=b; theme=dark", "allow", "no-cookie", 0},
		{"BearerToken only, no id token", "/" + instanceA + "/", "BearerToken=x", "login", "invalid-token", typev3.StatusCode_Found},
		{"expired token", "/" + instanceA + "/", idCookie + "=" + iss.token(t, nil, "", "", "alice", -time.Minute), "login", "invalid-token", typev3.StatusCode_Found},
		{"wrong audience", "/" + instanceA + "/", idCookie + "=" + iss.token(t, nil, "", "other", "alice", 0), "login", "invalid-token", typev3.StatusCode_Found},
		{"wrong issuer", "/" + instanceA + "/", idCookie + "=" + iss.token(t, nil, "https://evil.example.org", "", "alice", 0), "login", "invalid-token", typev3.StatusCode_Found},
		{"bad signature", "/" + instanceA + "/", idCookie + "=" + iss.token(t, otherKey, "", "", "alice", 0), "login", "invalid-token", typev3.StatusCode_Found},
		{"garbage token", "/" + instanceA + "/", idCookie + "=a.b.c", "login", "invalid-token", typev3.StatusCode_Found},
		{"owner on ready session", "/" + instanceA + "/foo?x=1", good, "allow", "owner", 0},
		{"quoted cookie value", "/" + instanceA + "/", `x=1; ` + idCookie + `="` + strings.TrimPrefix(good, idCookie+"=") + `"`, "allow", "owner", 0},
		{"foreign owner", "/" + instanceA + "/", idCookie + "=" + iss.token(t, nil, "", "", "mallory", 0), "deny", "not-owner", typev3.StatusCode_Forbidden},
		{"session not ready", "/bbbbbbbbbbbbbbbbbbbb/", good, "deny", "not-ready", typev3.StatusCode_Forbidden},
		{"session deleting", "/cccccccccccccccccccc/", good, "deny", "deleting", typev3.StatusCode_Forbidden},
		{"unknown instance", "/zzzzzzzzzzzzzzzzzzzz/", good, "deny", "unknown-instance", typev3.StatusCode_Forbidden},
		{"empty path", "", good, "deny", "bad-path", typev3.StatusCode_Forbidden},
		{"root path", "/", good, "deny", "unknown-instance", typev3.StatusCode_Forbidden},
		{"relative path", "x", "", "deny", "bad-path", typev3.StatusCode_Forbidden},
		{"dot-dot into other instance", "/" + instanceA + "/../bbbbbbbbbbbbbbbbbbbb/", good, "deny", "bad-path", typev3.StatusCode_Forbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := s.decide(context.Background(), req(tc.path, tc.cookie).GetAttributes().GetRequest().GetHttp())
			if d.result != tc.result || d.reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", d.result, d.reason, tc.result, tc.reason)
			}
			resp, err := s.Check(context.Background(), req(tc.path, tc.cookie))
			if err != nil {
				t.Fatal(err)
			}
			switch tc.result {
			case "allow":
				if resp.GetOkResponse() == nil {
					t.Fatal("want OkResponse")
				}
			case "deny":
				dr := resp.GetDeniedResponse()
				if dr == nil || dr.Status.Code != typev3.StatusCode_Forbidden || dr.Body != "" || len(dr.Headers) != 0 {
					t.Fatalf("want empty 403, got %v", resp)
				}
			case "login":
				dr := resp.GetDeniedResponse()
				if dr == nil || dr.Status.Code != typev3.StatusCode_Found {
					t.Fatalf("want 302, got %v", resp)
				}
			}
		})
	}
}

func TestLoginRedirect(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{})
	r := req("/"+instanceA+"/a/b?x=1&y=2", "BearerToken=t; OauthHMAC=h; "+idCookie+"=junk; keep=1")
	dr := s.mustCheck(t, r).GetDeniedResponse()
	if dr.Status.Code != typev3.StatusCode_Found {
		t.Fatalf("status %v", dr.Status.Code)
	}
	var loc string
	var cookies []string
	for _, h := range dr.Headers {
		switch h.Header.Key {
		case "Location":
			loc = h.Header.Value
		case "Set-Cookie":
			cookies = append(cookies, h.Header.Value)
		}
	}
	if loc != "https://"+instHost+"/"+instanceA+"/a/b?x=1&y=2" {
		t.Fatalf("location %q", loc)
	}
	if len(cookies) != 3 {
		t.Fatalf("cookies %v", cookies)
	}
	for _, c := range cookies {
		for _, want := range []string{"Domain=" + instHost, "Path=/", "Secure", "HttpOnly", "Max-Age=0"} {
			if !strings.Contains(c, want) {
				t.Errorf("cookie %q lacks %s", c, want)
			}
		}
		if strings.Contains(c, "keep=") {
			t.Errorf("unrelated cookie expired: %q", c)
		}
	}
	// A foreign host is replaced by the instance host.
	r.Attributes.Request.Http.Host = "evil.example.net"
	dr = s.mustCheck(t, r).GetDeniedResponse()
	if got := dr.Headers[0].Header.Value; !strings.HasPrefix(got, "https://"+instHost+"/") {
		t.Fatalf("location %q", got)
	}
}

func (s *Server) mustCheck(t *testing.T, r *authv3.CheckRequest) *authv3.CheckResponse {
	t.Helper()
	resp, err := s.Check(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFailClosed(t *testing.T) {
	iss := newIssuer(t)
	good := idCookie + "=" + iss.token(t, nil, "", "", "alice", 0)
	t.Run("lookup error", func(t *testing.T) {
		s := newServer(t, iss, fakeSessions{err: context.DeadlineExceeded})
		if d, _ := s.decide(context.Background(), req("/"+instanceA+"/", good).Attributes.Request.Http); d.result != "deny" || d.reason != "internal" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("no verifier yet", func(t *testing.T) {
		s := &Server{Sessions: fakeSessions{}, IDTokenCookie: idCookie}
		if d, _ := s.decide(context.Background(), req("/"+instanceA+"/", good).Attributes.Request.Http); d.result != "deny" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("nil request", func(t *testing.T) {
		s := newServer(t, iss, fakeSessions{})
		if _, ok := s.mustCheck(t, &authv3.CheckRequest{}).HttpResponse.(*authv3.CheckResponse_DeniedResponse); !ok {
			t.Fatal("want denied")
		}
	})
	t.Run("panicking lookup", func(t *testing.T) {
		s := newServer(t, iss, panicSessions{})
		if _, ok := s.mustCheck(t, req("/"+instanceA+"/", good)).HttpResponse.(*authv3.CheckResponse_DeniedResponse); !ok {
			t.Fatal("want denied")
		}
	})
}

type panicSessions struct{}

func (panicSessions) ByInstance(context.Context, string) ([]v1alpha1.IDESession, error) {
	panic("boom")
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		in, clean, first string
		ok               bool
	}{
		{"/abc/def?x=1", "/abc/def", "abc", true},
		{"/abc", "/abc", "abc", true},
		{"/abc#frag", "/abc", "abc", true},
		{"/abc//d/", "/abc/d", "abc", true},
		{"/abc/%2E%2E/abc/x", "/abc/x", "abc", true},
		{"//abc//d/", "", "", false},
		{"/%61bc/", "", "", false},
		{"/a/../b/", "", "", false},
		{"/a/..%2Fb/", "", "", false},
		{"/a%2F..%2Fb/", "", "", false},
		{"/./b/", "", "", false},
		{"/", "/", "", true},
		{"", "", "", false},
		{"abc", "", "", false},
		{"/a%zz", "", "", false},
		{"/a%0d%0ab", "", "", false},
		{"/a\\b", "", "", false},
	}
	for _, tc := range tests {
		c, f, ok := splitPath(tc.in)
		if c != tc.clean || f != tc.first || ok != tc.ok {
			t.Errorf("splitPath(%q) = %q %q %v", tc.in, c, f, ok)
		}
	}
}

func TestParseCookies(t *testing.T) {
	got := parseCookies([]string{`a=1; b="two"`, `c=3, d=4`, ` e = 5 ;;novalue; =x`, ""})
	want := []cookie{{"a", "1"}, {"b", "two"}, {"c", "3"}, {"d", "4"}, {"e", "5"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v want %v", got[i], want[i])
		}
	}
}

func TestMultipleCookieHeadersViaHeaderMap(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)}}})
	r := req("/"+instanceA+"/", "")
	r.Attributes.Request.Http.HeaderMap = headerMap("Cookie", "a=b", "cookie", idCookie+"="+iss.token(t, nil, "", "", "alice", 0))
	if s.mustCheck(t, r).GetOkResponse() == nil {
		t.Fatal("want allow")
	}
}

func TestGRPCRoundTrip(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)}}})
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	authv3.RegisterAuthorizationServer(g, s)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := authv3.NewAuthorizationClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Check(ctx, req("/"+instanceA+"/", idCookie+"="+iss.token(t, nil, "", "", "alice", 0)))
	if err != nil || resp.GetOkResponse() == nil || resp.Status.Code != 0 {
		t.Fatalf("allow: %v %v", resp, err)
	}
	resp, err = c.Check(ctx, req("/"+instanceA+"/", idCookie+"="+iss.token(t, nil, "", "", "bob", 0)))
	if err != nil || resp.GetDeniedResponse().GetStatus().GetCode() != typev3.StatusCode_Forbidden {
		t.Fatalf("deny: %v %v", resp, err)
	}
	resp, err = c.Check(ctx, req("/"+instanceA+"/", idCookie+"=junk"))
	if err != nil || resp.GetDeniedResponse().GetStatus().GetCode() != typev3.StatusCode_Found {
		t.Fatalf("login: %v %v", resp, err)
	}
}

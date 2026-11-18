package authz

import (
	"context"
	"strings"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func headerMap(kv ...string) *corev3.HeaderMap {
	m := &corev3.HeaderMap{}
	for i := 0; i < len(kv); i += 2 {
		m.Headers = append(m.Headers, &corev3.HeaderValue{Key: kv[i], Value: kv[i+1]})
	}
	return m
}

func FuzzSplitPath(f *testing.F) {
	for _, s := range []string{"/a/b?x", "", "/", "//", "/%", "/a/../..", "/\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		clean, first, ok := splitPath(in)
		if ok && (clean == "" || clean[0] != '/' || len(first) > len(clean)) {
			t.Fatalf("bad result %q %q for %q", clean, first, in)
		}
	})
}

func FuzzParseCookies(f *testing.F) {
	for _, s := range []string{"a=b", `a="b"; c=d`, ";;,,", "=", `"`, "a=b, c=d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, c := range parseCookies([]string{in}) {
			if c.name == "" {
				t.Fatal("empty name")
			}
		}
	})
}

// FuzzCheck: Check never panics, and without a valid owner token only the
// oauth2 and no-cookie rows allow.
func FuzzCheck(f *testing.F) {
	iss := newIssuer(f)
	s := newServer(f, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)}}})
	good := iss.token(f, nil, "", "", "alice", 0)
	f.Add("/"+instanceA+"/", idCookie+"="+good, "", "ide.example.org")
	f.Add("/oauth2/callback", "", "", "")
	f.Add("/"+instanceA+"/", "BearerToken=x", idCookie+"=junk", "evil")
	f.Add("", `"`, ";", "")
	f.Add("/zzzzzzzzzzzzzzzzzzzz/..%2F"+instanceA+"/", idCookie+"="+good, "", "")
	f.Add("/zzzzzzzzzzzzzzzzzzzz/..%2Foauth2/x", idCookie+"="+good, "", "")
	f.Fuzz(func(t *testing.T, path, cookie, cookie2, host string) {
		r := &authv3.AttributeContext_HttpRequest{Path: path, Host: host,
			Headers: map[string]string{"cookie": cookie}, HeaderMap: headerMap("cookie", cookie, "Cookie", cookie2)}
		resp, err := s.Check(context.Background(), &authv3.CheckRequest{Attributes: &authv3.AttributeContext{Request: &authv3.AttributeContext_Request{Http: r}}})
		if err != nil || resp == nil {
			t.Fatalf("err %v resp %v", err, resp)
		}
		if resp.GetOkResponse() == nil {
			return
		}
		all := cookie + ";" + cookie2
		clean, _, ok := splitPath(path)
		// Envoy matches the route on the literal path.
		literal := strings.SplitN(strings.SplitN(path, "?", 2)[0], "/", 3)
		switch {
		case !ok:
			t.Fatalf("allowed bad path %q", path)
		case strings.HasPrefix(path, "/oauth2/") && strings.HasPrefix(clean, "/oauth2/"):
		case !hasAuthCookie(parseCookies([]string{cookie, cookie2}), s.IDTokenCookie):
		case len(literal) > 1 && literal[1] == instanceA && strings.Contains(all, good):
		default:
			t.Fatalf("unexpected allow: path %q cookies %q %q", path, cookie, cookie2)
		}
	})
}

func hasAuthCookie(cs []cookie, idCookie string) bool {
	for _, c := range cs {
		if isAuthCookie(c.name, idCookie) {
			return true
		}
	}
	return false
}

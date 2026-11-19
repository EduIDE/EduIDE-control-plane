package agent

import (
	"net/http"
	"testing"
)

func TestStripAuthCookies(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"eduide-id=x; theme=dark"}, "theme=dark"},
		{[]string{"AccessToken-ab12=x", "OauthHMAC-ab12=y; a=1"}, "a=1"},
		{[]string{"RefreshToken-1=x; IdToken-1=y; OauthExpires-1=z; OauthNonce=n; CodeVerifier=c"}, ""},
		{[]string{"eduide-authz=a.b.c.d; theme=dark"}, "theme=dark"},
		{[]string{"a=1;b=2"}, "a=1; b=2"},
	} {
		h := http.Header{"Cookie": tc.in}
		stripAuthCookies(h)
		if got := h.Get("Cookie"); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The ID token cookie has a configurable name (oidc.idTokenCookie); the agent must strip that one, too.
func TestStripCookieAddsTheConfiguredName(t *testing.T) {
	saved := authCookiePrefixes
	t.Cleanup(func() { authCookiePrefixes = saved })
	h := http.Header{"Cookie": {"uni-id=ID.TOKEN.JWT; theme=dark"}}
	stripAuthCookies(h)
	if got := h.Get("Cookie"); got != "uni-id=ID.TOKEN.JWT; theme=dark" {
		t.Fatalf("before: %q", got)
	}
	StripCookie("uni-id")
	StripCookie("uni-id")
	StripCookie("eduide-id")
	StripCookie("")
	if len(authCookiePrefixes) != len(saved)+1 {
		t.Fatalf("list: %v", authCookiePrefixes)
	}
	stripAuthCookies(h)
	if got := h.Get("Cookie"); got != "theme=dark" {
		t.Fatalf("after: %q", got)
	}
	// Cookies the IDE sets under that name are dropped as well.
	rh := http.Header{"Set-Cookie": {"uni-id=x; Path=/", "theme=dark"}}
	scopeCookies(rh, "/abc/")
	if got := rh.Values("Set-Cookie"); len(got) != 1 || got[0] != "theme=dark" {
		t.Fatalf("set-cookie: %q", got)
	}
}

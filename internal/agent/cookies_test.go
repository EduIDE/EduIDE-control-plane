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

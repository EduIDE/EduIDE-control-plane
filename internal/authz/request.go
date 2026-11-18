package authz

import (
	"net/url"
	"path"
	"strings"
)

// authCookiePrefixes are the cookie name prefixes of the gateway OIDC filter.
var authCookiePrefixes = []string{"BearerToken", "OauthHMAC", "OauthExpires", "IdToken", "RefreshToken"}

// splitPath returns the cleaned, unescaped path (without query) and its first
// segment. ok is false for anything that is not an absolute, sane path.
func splitPath(raw string) (clean, first string, ok bool) {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if !strings.HasPrefix(raw, "/") {
		return "", "", false
	}
	p, err := url.PathUnescape(raw)
	if err != nil || strings.ContainsAny(p, "\x00\r\n\\") {
		return "", "", false
	}
	clean = path.Clean(p)
	rest := strings.TrimPrefix(clean, "/")
	first, _, _ = strings.Cut(rest, "/")
	return clean, first, true
}

type cookie struct{ name, value string }

// parseCookies reads every Cookie header leniently: it splits on ';' and ',' (Envoy
// joins repeated headers with a comma), trims blanks and strips one pair of quotes.
func parseCookies(headers []string) []cookie {
	var out []cookie
	for _, h := range headers {
		for _, part := range strings.FieldsFunc(h, func(r rune) bool { return r == ';' || r == ',' }) {
			name, value, found := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.TrimSpace(name)
			if !found || name == "" {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
				value = value[1 : len(value)-1]
			}
			out = append(out, cookie{name, value})
		}
	}
	return out
}

func isAuthCookie(name, idTokenCookie string) bool {
	if name == idTokenCookie {
		return true
	}
	for _, p := range authCookiePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

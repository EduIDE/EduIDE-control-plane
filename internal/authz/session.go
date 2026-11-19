package authz

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The authz session cookie closes a gap of ADR 0005: Envoy sets the Max-Age of the ID token cookie to the token
// lifetime (Keycloak default 5 minutes), so the browser drops it, and because ext_authz runs before the oauth2
// filter, the refresh with the refresh token never got a chance. While the ID token is valid, authz sets this
// cookie with the owner, signed with SessionKey and bound to a hash of the (encrypted) refresh token cookie.
// Without an ID token, authz takes the owner from it only with that same refresh token; the oauth2 filter that
// follows then refreshes the tokens or, when Keycloak refuses, restarts the login. So the cookie never extends
// a Keycloak session, and it is useless without the refresh token it is bound to.
const (
	sessionCookie   = "eduide-authz"
	sessionLifetime = 12 * time.Hour
	// sessionRenew renews a cookie that would expire within this time.
	sessionRenew = 6 * time.Hour
)

var b64 = base64.RawURLEncoding

// SessionKey derives the cookie key from the user salt, so all authz replicas share it without a new Secret.
// No salt, no key: the cookie stays off.
func SessionKey(salt []byte) []byte {
	if len(salt) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, salt)
	m.Write([]byte("eduide authz session cookie v1"))
	return m.Sum(nil)
}

// binding hashes the refresh token cookie; exactly one non-empty refresh token cookie is required.
func binding(refresh []string) (string, bool) {
	if len(refresh) != 1 || refresh[0] == "" {
		return "", false
	}
	h := sha256.Sum256([]byte(refresh[0]))
	return b64.EncodeToString(h[:16]), true
}

func (s *Server) sign(payload string) string {
	m := hmac.New(sha256.New, s.SessionKey)
	m.Write([]byte(payload))
	return b64.EncodeToString(m.Sum(nil))
}

// sessionOwner returns the owner of a valid session cookie that is bound to the refresh token of this request.
func (s *Server) sessionOwner(value string, refresh []string) (string, bool) {
	owner, exp, ok := s.parseSession(value, refresh)
	return owner, ok && time.Now().Before(exp)
}

func (s *Server) parseSession(value string, refresh []string) (owner string, exp time.Time, ok bool) {
	bind, ok := binding(refresh)
	if len(s.SessionKey) == 0 || !ok {
		return "", time.Time{}, false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return "", time.Time{}, false
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(s.sign(payload)), []byte(parts[3])) || parts[2] != bind {
		return "", time.Time{}, false
	}
	o, err := b64.DecodeString(parts[0])
	sec, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || err2 != nil || len(o) == 0 {
		return "", time.Time{}, false
	}
	return string(o), time.Unix(sec, 0), true
}

// renewSession returns a Set-Cookie value when the request's session cookie is missing, belongs to another owner
// or refresh token, or expires soon; otherwise "".
func (s *Server) renewSession(value, owner string, refresh []string) string {
	bind, ok := binding(refresh)
	if len(s.SessionKey) == 0 || !ok {
		return ""
	}
	if o, exp, ok := s.parseSession(value, refresh); ok && o == owner && time.Until(exp) > sessionRenew {
		return ""
	}
	payload := b64.EncodeToString([]byte(owner)) + "." + strconv.FormatInt(time.Now().Add(sessionLifetime).Unix(), 10) + "." + bind
	c := &http.Cookie{Name: sessionCookie, Value: payload + "." + s.sign(payload), Path: "/", Domain: s.InstanceHost,
		MaxAge: int(sessionLifetime.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	return c.String()
}

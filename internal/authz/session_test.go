package authz

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// sessionValue builds a session cookie value the way renewSession does, with a chosen expiry and refresh token.
func sessionValue(s *Server, owner string, exp time.Time, refresh string) string {
	bind, _ := binding([]string{refresh})
	payload := b64.EncodeToString([]byte(owner)) + "." + strconv.FormatInt(exp.Unix(), 10) + "." + bind
	return payload + "." + s.sign(payload)
}

func TestSessionCookie(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{
		instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)},
	}})
	s.SessionKey = SessionKey(s.Salt)
	good := idCookie + "=" + iss.token(t, nil, "", "", "alice", 0)
	refresh := "RefreshToken-abc=r1"
	in := time.Now().Add(sessionLifetime)
	sess := func(owner string, exp time.Time, rt string) string {
		return sessionCookie + "=" + sessionValue(s, owner, exp, rt)
	}
	tampered := sessionValue(s, "mallory", in, "r1")
	tampered = b64.EncodeToString([]byte("alice")) + tampered[strings.Index(tampered, "."):]
	tests := []struct {
		name, cookie   string
		result, reason string
		setsCookie     bool
	}{
		{"valid id token issues the cookie", good + "; " + refresh, "allow", "owner", true},
		{"valid id token without refresh token issues none", good, "allow", "owner", false},
		{"fresh cookie is not renewed", good + "; " + refresh + "; " + sess("alice", in, "r1"), "allow", "owner", false},
		{"cookie close to expiry is renewed", good + "; " + refresh + "; " + sess("alice", time.Now().Add(time.Hour), "r1"), "allow", "owner", true},
		{"cookie of a rotated refresh token is renewed", good + "; RefreshToken-abc=r2; " + sess("alice", in, "r1"), "allow", "owner", true},
		{"no id token, cookie bound to this refresh token", refresh + "; " + sess("alice", in, "r1"), "allow", "owner", false},
		{"expired id token cookie, valid session", idCookie + "=" + iss.token(t, nil, "", "", "alice", -time.Minute) + "; " + refresh + "; " + sess("alice", in, "r1"), "allow", "owner", false},
		{"no id token, cookie bound to another refresh token", "RefreshToken-abc=other; " + sess("alice", in, "r1"), "login", "invalid-token", false},
		{"no id token, cookie without refresh token", sess("alice", in, "r1"), "login", "invalid-token", false},
		{"no id token, two refresh tokens", refresh + "; RefreshToken-def=r1; " + sess("alice", in, "r1"), "login", "invalid-token", false},
		{"no id token, expired cookie", refresh + "; " + sess("alice", time.Now().Add(-time.Second), "r1"), "login", "invalid-token", false},
		{"no id token, tampered owner", refresh + "; " + sessionCookie + "=" + tampered, "login", "invalid-token", false},
		{"no id token, garbage cookie", refresh + "; " + sessionCookie + "=a.b.c.d", "login", "invalid-token", false},
		{"no id token, foreign owner", refresh + "; " + sess("mallory", in, "r1"), "deny", "not-owner", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := s.decide(context.Background(), req("/"+instanceA+"/", tc.cookie).GetAttributes().GetRequest().GetHttp())
			if d.result != tc.result || d.reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", d.result, d.reason, tc.result, tc.reason)
			}
			if (d.setCookie != "") != tc.setsCookie {
				t.Fatalf("setCookie %q, want set=%v", d.setCookie, tc.setsCookie)
			}
			if d.setCookie == "" {
				return
			}
			for _, attr := range []string{sessionCookie + "=", "Domain=" + instHost, "HttpOnly", "Secure", "SameSite=Lax", "Max-Age=43200"} {
				if !strings.Contains(d.setCookie, attr) {
					t.Errorf("cookie %q lacks %s", d.setCookie, attr)
				}
			}
			resp, err := s.Check(context.Background(), req("/"+instanceA+"/", tc.cookie))
			if err != nil {
				t.Fatal(err)
			}
			hs := resp.GetOkResponse().GetResponseHeadersToAdd()
			if len(hs) != 1 || hs[0].GetHeader().GetKey() != "Set-Cookie" {
				t.Fatalf("OkResponse headers %v", hs)
			}
			// The issued cookie works on its own with the same refresh token.
			v := strings.TrimPrefix(strings.SplitN(d.setCookie, ";", 2)[0], sessionCookie+"=")
			rt := strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(tc.cookie, good+"; "), ";", 2)[0], "RefreshToken-abc=")
			if o, ok := s.sessionOwner(v, []string{rt}); !ok || o != "alice" {
				t.Fatalf("issued cookie does not verify: %q %v", o, ok)
			}
		})
	}
}

func TestSessionCookieOffWithoutKey(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{
		instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)},
	}})
	if SessionKey(nil) != nil {
		t.Fatal("key without salt")
	}
	s.SessionKey = SessionKey([]byte("salt"))
	forged := sessionValue(s, "alice", time.Now().Add(time.Hour), "r1")
	s.SessionKey = nil
	d, _ := s.decide(context.Background(), req("/"+instanceA+"/", idCookie+"="+iss.token(t, nil, "", "", "alice", 0)+"; RefreshToken-abc=r1").GetAttributes().GetRequest().GetHttp())
	if d.result != "allow" || d.setCookie != "" {
		t.Fatalf("got %s with cookie %q", d.result, d.setCookie)
	}
	d, _ = s.decide(context.Background(), req("/"+instanceA+"/", "RefreshToken-abc=r1; "+sessionCookie+"="+forged).GetAttributes().GetRequest().GetHttp())
	if d.result != "login" {
		t.Fatalf("got %s, want login", d.result)
	}
}

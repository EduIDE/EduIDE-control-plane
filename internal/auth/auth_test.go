package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

type issuer struct {
	srv *httptest.Server
	key *ecdsa.PrivateKey
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i := &issuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": i.srv.URL, "jwks_uri": i.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"ES256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k", Algorithm: "ES256", Use: "sig"}}})
	})
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

func (i *issuer) token(t *testing.T, iss string, claims map[string]any) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: i.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k"))
	if err != nil {
		t.Fatal(err)
	}
	std := jwt.Claims{Issuer: iss, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now())}
	raw, err := jwt.Signed(sig).Claims(std).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOIDCVerify(t *testing.T) {
	is := newIssuer(t)
	v := NewOIDC(config.OIDC{IssuerURL: is.srv.URL, ClientIDs: []string{"landing", "cli"}, UserClaim: "preferred_username", NameClaim: "name", EmailClaim: "email"})
	user := map[string]any{"preferred_username": "alice", "name": "Alice A", "email": "alice@example.org"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range user {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name   string
		iss    string
		claims map[string]any
		ok     bool
	}{
		{"aud accepted", is.srv.URL, with(map[string]any{"aud": "landing"}), true},
		{"azp accepted", is.srv.URL, with(map[string]any{"aud": "account", "azp": "cli"}), true},
		{"one of many aud", is.srv.URL, with(map[string]any{"aud": []string{"x", "landing"}}), true},
		{"foreign audience", is.srv.URL, with(map[string]any{"aud": "other", "azp": "other"}), false},
		{"other client with our aud", is.srv.URL, with(map[string]any{"aud": []string{"landing"}, "azp": "other"}), false},
		{"expired", is.srv.URL, with(map[string]any{"aud": "landing", "exp": time.Now().Add(-time.Minute).Unix()}), false},
		{"no audience", is.srv.URL, user, false},
		{"wrong issuer", "https://evil.example.com", with(map[string]any{"aud": "landing"}), false},
		{"no user claim", is.srv.URL, map[string]any{"aud": "landing", "name": "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := v.Verify(context.Background(), is.token(t, tt.iss, tt.claims))
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && (u.Owner != "alice" || u.Name != "Alice A" || u.Email != "alice@example.org") {
				t.Fatalf("user %+v", u)
			}
		})
	}
	if _, err := v.Verify(context.Background(), "garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}

type stub struct{ u User }

func (s stub) Verify(_ context.Context, raw string) (User, error) {
	if raw == "good" {
		return s.u, nil
	}
	return User{}, context.Canceled
}

func TestMiddleware(t *testing.T) {
	var got User
	h := Middleware(stub{User{Owner: "alice"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = FromContext(r.Context())
	}))
	for _, hdr := range []string{"", "Bearer ", "Basic good", "Bearer bad"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		h.ServeHTTP(rec, req)
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != 401 || p["code"] != "unauthorized" || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%q: %d %s", hdr, rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || got.Owner != "alice" {
		t.Fatalf("%d %+v", rec.Code, got)
	}
}

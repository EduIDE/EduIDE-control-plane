// Package auth verifies OIDC bearer tokens of the REST API.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

// User is the caller, taken from a verified token.
type User struct {
	Owner string
	Name  string
	Email string
}

// Verifier turns a raw bearer token into a User.
type Verifier interface {
	Verify(ctx context.Context, raw string) (User, error)
}

type ctxKey struct{}

// WithUser returns a context that carries u.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext returns the user the middleware stored.
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// OIDC verifies tokens against the issuer's JWKS (cached by go-oidc).
type OIDC struct {
	cfg config.OIDC

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

func NewOIDC(cfg config.OIDC) *OIDC { return &OIDC{cfg: cfg} }

// init discovers the issuer on first use, so the service starts while the issuer is down.
func (o *OIDC) init(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.verifier != nil {
		return o.verifier, nil
	}
	p, err := oidc.NewProvider(ctx, o.cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	// The audience is checked below: accept azp or any aud.
	o.verifier = p.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return o.verifier, nil
}

func (o *OIDC) Verify(ctx context.Context, raw string) (User, error) {
	v, err := o.init(ctx)
	if err != nil {
		return User{}, err
	}
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return User{}, err
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return User{}, err
	}
	azp, _ := claims["azp"].(string)
	if !slices.ContainsFunc(append([]string{azp}, tok.Audience...), func(a string) bool {
		return a != "" && slices.Contains(o.cfg.ClientIDs, a)
	}) {
		return User{}, errors.New("token audience not accepted")
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	u := User{Owner: str(o.cfg.UserClaim), Name: str(o.cfg.NameClaim), Email: str(o.cfg.EmailClaim)}
	if u.Owner == "" {
		return User{}, fmt.Errorf("token has no %s claim", o.cfg.UserClaim)
	}
	return u, nil
}

// Middleware requires a valid bearer token and stores the User in the request context.
func Middleware(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				unauthorized(w)
				return
			}
			u, err := v.Verify(r.Context(), raw)
			if err != nil {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://eduide.github.io/problems/unauthorized",
		"title":  "Unauthorized",
		"status": http.StatusUnauthorized,
		"code":   "unauthorized",
	})
}

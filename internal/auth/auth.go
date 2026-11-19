// Package auth verifies OIDC bearer tokens of the REST API.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
)

// User is the caller, taken from a verified token.
type User struct {
	Owner string
	Name  string
	Email string
	// Roles are the roles of the token at the configured admin role path (U16); nil without oidc.admin.
	Roles []string
}

// RolesAt reads the role list of the claims at rolePath, which config.Parse restricts to
// "realm_access.roles" and "resource_access.<client>.roles". The client id may contain dots. Anything else,
// and a role list that is not a JSON array, yields no roles.
func RolesAt(claims map[string]any, rolePath string) []string {
	var list any
	if rolePath == "realm_access.roles" {
		realm, _ := claims["realm_access"].(map[string]any)
		list = realm["roles"]
	} else if rest, ok := strings.CutPrefix(rolePath, "resource_access."); ok {
		client, ok := strings.CutSuffix(rest, ".roles")
		if !ok || client == "" {
			return nil
		}
		clients, _ := claims["resource_access"].(map[string]any)
		c, _ := clients[client].(map[string]any)
		list = c["roles"]
	}
	roles, _ := list.([]any)
	var out []string
	for _, r := range roles {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
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

// discoveryLog keeps a down issuer from logging one line per request.
var discoveryLog = logx.NewLimiter(6)

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
	// The lock is held during discovery; a bound keeps a slow issuer from queueing every request.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, o.cfg.IssuerURL)
	if err != nil {
		if ok, n := discoveryLog.Allow("discovery"); ok {
			slog.WarnContext(ctx, "oidc discovery failed", "issuer", o.cfg.IssuerURL, "error", err.Error(), "suppressed", n)
		}
		return nil, err
	}
	slog.InfoContext(ctx, "ready", "detail", "oidc discovered", "issuer", o.cfg.IssuerURL)
	// The audience is checked below: the azp, or without azp any aud, must be one of ours.
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
	// A token another client of the realm obtained (its azp) is never ours, even if it lists our client in aud.
	accepted := tok.Audience
	if azp, _ := claims["azp"].(string); azp != "" {
		accepted = []string{azp}
	}
	if !slices.ContainsFunc(accepted, func(a string) bool { return slices.Contains(o.cfg.ClientIDs, a) }) {
		return User{}, errors.New("token audience not accepted")
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	u := User{Owner: str(o.cfg.UserClaim), Name: str(o.cfg.NameClaim), Email: str(o.cfg.EmailClaim)}
	if u.Owner == "" {
		return User{}, fmt.Errorf("token has no %s claim", o.cfg.UserClaim)
	}
	if o.cfg.Admin != nil {
		u.Roles = RolesAt(claims, o.cfg.Admin.RolePath)
	}
	return u, nil
}

// Middleware requires a valid bearer token and stores the User in the request context.
func Middleware(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				unauthorized(w, "Bearer")
				return
			}
			u, err := v.Verify(r.Context(), raw)
			if err != nil {
				slog.DebugContext(r.Context(), "bearer token rejected", "error", err.Error())
				unauthorized(w, `Bearer error="invalid_token"`) // RFC 6750
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
		})
	}
}

func unauthorized(w http.ResponseWriter, challenge string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://eduide.github.io/problems/unauthorized",
		"title":  "Unauthorized",
		"status": http.StatusUnauthorized,
		"code":   "unauthorized",
	})
}

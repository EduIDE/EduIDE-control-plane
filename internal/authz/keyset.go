package authz

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

// KeySet verifies signatures with the issuer's JWKS. Unlike go-oidc's RemoteKeySet
// it refetches the JWKS at most once per Interval, so tokens with forged key ids
// cannot make authz hammer the IdP. A rotated key is picked up within Interval.
type KeySet struct {
	URL      string
	Client   *http.Client
	Interval time.Duration

	keys    atomic.Pointer[oidc.StaticKeySet]
	mu      sync.Mutex // serializes fetches and guards fetched
	fetched time.Time
}

func (k *KeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	if s := k.keys.Load(); s != nil {
		if p, err := s.VerifySignature(ctx, jwt); err == nil {
			return p, nil
		}
	}
	k.refresh(ctx)
	s := k.keys.Load()
	if s == nil {
		return nil, errors.New("no signing keys")
	}
	return s.VerifySignature(ctx, jwt)
}

func (k *KeySet) refresh(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.fetched.IsZero() && time.Since(k.fetched) < k.Interval {
		return
	}
	k.fetched = time.Now()
	keys, err := k.fetch(context.WithoutCancel(ctx))
	if err != nil {
		slog.WarnContext(ctx, "jwks fetch failed", "error", err)
		return
	}
	k.keys.Store(&oidc.StaticKeySet{PublicKeys: keys})
}

func (k *KeySet) fetch(ctx context.Context) ([]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	var set struct{ Keys []json.RawMessage }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	var out []crypto.PublicKey
	for _, raw := range set.Keys {
		var j jose.JSONWebKey
		if j.UnmarshalJSON(raw) != nil || j.Use == "enc" {
			continue // skip key types jose does not know and encryption keys
		}
		switch j.Key.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
			out = append(out, j.Key)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks has no signing keys")
	}
	return out, nil
}

// asymmetricAlgs are the ID token algorithms a JWKS of public keys can verify.
var asymmetricAlgs = map[string]bool{"RS256": true, "RS384": true, "RS512": true, "PS256": true, "PS384": true,
	"PS512": true, "ES256": true, "ES384": true, "ES512": true, "EdDSA": true}

// Verifier builds the ID token verifier for p on a rate-limited KeySet.
func Verifier(p *oidc.Provider, clientID string, client *http.Client, interval time.Duration) (*oidc.IDTokenVerifier, error) {
	var c struct {
		Issuer string   `json:"issuer"`
		JWKS   string   `json:"jwks_uri"`
		Algs   []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := p.Claims(&c); err != nil {
		return nil, err
	}
	var algs []string // empty means RS256 in go-oidc
	for _, a := range c.Algs {
		if asymmetricAlgs[a] {
			algs = append(algs, a)
		}
	}
	ks := &KeySet{URL: c.JWKS, Client: client, Interval: interval}
	// Server.owner checks the expiry itself, see expiredGrace.
	return oidc.NewVerifier(c.Issuer, ks, &oidc.Config{ClientID: clientID, SupportedSigningAlgs: algs, SkipExpiryCheck: true}), nil
}

// Package gitaccount connects users to GitHub and GitLab accounts (ADR 0023): OAuth clients per provider type,
// the encrypted token store and the refresh logic. Tokens leave this package only through Manager.Token.
package gitaccount

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

// Token is what a provider hands out.
type Token struct {
	Access  string
	Refresh string    // empty for GitHub OAuth App tokens
	Expiry  time.Time // zero when the token does not expire
}

// Account is the identity behind a token.
type Account struct {
	ID          int64
	Login       string
	DisplayName string
	// NoReplyEmail is the commit address the platform offers to hide the private one.
	NoReplyEmail string
}

var (
	// ErrInvalidGrant means the code or refresh token is no longer valid, for example revoked at the host.
	ErrInvalidGrant = errors.New("invalid_grant")
	// ErrNoRefresh means the provider issues no refresh tokens (GitHub OAuth App).
	ErrNoRefresh = errors.New("provider has no refresh tokens")
)

// Provider is the OAuth client for one configured Git host. Errors carry the provider's short error code
// and the HTTP status, never a token, a code or provider text.
type Provider interface {
	AuthorizeURL(redirectURI, state, challenge string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error)
	Refresh(ctx context.Context, refreshToken string) (Token, error)
	Account(ctx context.Context, accessToken string) (Account, error)
	// Revoke is best effort: an already revoked token is not an error.
	Revoke(ctx context.Context, t Token) error
}

// Endpoints are the base URLs of a provider; tests point them at a fake.
type Endpoints struct {
	// Web serves the OAuth endpoints, API the user lookup (and the GitHub revoke).
	Web, API string
}

// DefaultEndpoints derives the URLs from the host.
func DefaultEndpoints(p config.GitProvider) Endpoints {
	e := Endpoints{Web: "https://" + p.Host}
	switch {
	case p.Type == config.GitLab:
		e.API = e.Web + "/api/v4"
	case strings.EqualFold(p.Host, "github.com"):
		e.API = "https://api.github.com"
	default:
		e.API = e.Web + "/api/v3"
	}
	return e
}

// NewProvider builds the client for p. A nil hc gets a client with a timeout that follows no redirects, so a
// request with a client secret never leaves the configured host.
func NewProvider(p config.GitProvider, secret string, e Endpoints, hc *http.Client) Provider {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	c := &oauth{cfg: p, secret: secret, ep: e, hc: hc}
	if p.Type == config.GitLab {
		return &gitlab{c}
	}
	return &github{c}
}

// ChallengeOf is the S256 code challenge of a PKCE verifier.
func ChallengeOf(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// errUnwrap drops the request URL a transport error carries; the cause is enough.
func errUnwrap(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type oauth struct {
	cfg    config.GitProvider
	secret string
	ep     Endpoints
	hc     *http.Client
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

var errCodeRE = regexp.MustCompile(`^[a-z_]{1,40}$`)

// providerError keeps only a short machine code of the provider's answer.
func providerError(op string, status int, code string) error {
	if code == "invalid_grant" || code == "bad_verification_code" {
		return fmt.Errorf("%s: %w", op, ErrInvalidGrant)
	}
	if !errCodeRE.MatchString(code) {
		code = ""
	}
	return fmt.Errorf("%s: status %d %s", op, status, code)
}

// do sends the request and reads at most 1 MiB of the answer.
func (c *oauth) do(req *http.Request) (int, []byte, error) {
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req) // #nosec G704 -- the host comes from the deployment config, never from a user
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, err
}

func (c *oauth) form(ctx context.Context, op, u string, v url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(v.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, b, err := c.do(req)
	if err != nil {
		return Token{}, fmt.Errorf("%s: %w", op, errUnwrap(err))
	}
	var tr tokenResponse
	_ = json.Unmarshal(b, &tr)
	// GitHub reports errors with status 200.
	if status != http.StatusOK || tr.Error != "" || tr.AccessToken == "" {
		return Token{}, providerError(op, status, tr.Error)
	}
	t := Token{Access: tr.AccessToken, Refresh: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// getJSON calls an API endpoint with the access token.
func (c *oauth) getJSON(ctx context.Context, op, u, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	status, b, err := c.do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, errUnwrap(err))
	}
	if status != http.StatusOK {
		return providerError(op, status, "")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: unreadable answer", op)
	}
	return nil
}

func (c *oauth) authorize(path, scope, redirectURI, state, challenge string, extra url.Values) string {
	q := url.Values{"client_id": {c.cfg.ClientID}, "redirect_uri": {redirectURI}, "state": {state}, "scope": {scope},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	for k, v := range extra {
		q[k] = v
	}
	return c.ep.Web + path + "?" + q.Encode()
}

func (c *oauth) exchange(ctx context.Context, path, code, verifier, redirectURI string) (Token, error) {
	return c.form(ctx, "exchange", c.ep.Web+path, url.Values{"client_id": {c.cfg.ClientID}, "client_secret": {c.secret},
		"code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}})
}

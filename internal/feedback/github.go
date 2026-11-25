package feedback

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Issue is what the receiver files in the triage repository.
type Issue struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// GitHub creates issues as a GitHub App installation. It signs the app JWT itself (RS256, standard library only)
// and caches the installation token until shortly before it expires.
type GitHub struct {
	APIURL         string // for example https://api.github.com
	AppID          string
	InstallationID string
	Repository     string // owner/name
	Key            *rsa.PrivateKey
	HTTP           *http.Client
	Now            func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// ParseKey reads an RSA private key in PKCS#1 or PKCS#8 PEM form, as GitHub hands it out.
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rk, nil
}

func (g *GitHub) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// appJWT signs the short-lived token that authenticates the app itself. GitHub allows at most 10 minutes
// and wants iat in the past to absorb clock drift.
func (g *GitHub) appJWT() (string, error) {
	now := g.now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": g.AppID})
	signing := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

func (g *GitHub) do(ctx context.Context, method, path, bearer string, in, out any, want int) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	// The base URL is operator configuration, not request input.
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.APIURL, "/")+path, body) //nolint:gosec // G704: URL from operator configuration
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "eduide-feedback-receiver")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := c.Do(req) //nolint:gosec // G704: URL from operator configuration
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return &statusError{path: path, status: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// statusError carries only the status, never the response body.
type statusError struct {
	path   string
	status int
}

func (e *statusError) Error() string { return fmt.Sprintf("github %s answered %d", e.path, e.status) }

func (g *GitHub) installationToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && g.now().Before(g.expires.Add(-5*time.Minute)) {
		return g.token, nil
	}
	jwt, err := g.appJWT()
	if err != nil {
		return "", err
	}
	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := g.do(ctx, http.MethodPost, "/app/installations/"+g.InstallationID+"/access_tokens", jwt, nil, &res, http.StatusCreated); err != nil {
		return "", err
	}
	g.token, g.expires = res.Token, res.ExpiresAt
	return g.token, nil
}

// CreateIssue files the issue and returns its number.
func (g *GitHub) CreateIssue(ctx context.Context, in Issue) (int, error) {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return 0, err
	}
	var res struct {
		Number int `json:"number"`
	}
	err = g.do(ctx, http.MethodPost, "/repos/"+g.Repository+"/issues", tok, in, &res, http.StatusCreated)
	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusUnauthorized {
		// The cached token was revoked: fetch a new one on the next request.
		g.mu.Lock()
		g.token = ""
		g.mu.Unlock()
	}
	return res.Number, err
}

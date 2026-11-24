package gitproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

const (
	// allowTTL is how long an allowed answer with a credential is reused. It bounds how long a disconnected account
	// or a removed approval keeps working in a running session, and it stays far below the token lifetime
	// (the service refreshes a token a minute before it expires). One authorize call per five seconds and
	// repository is cheap.
	allowTTL = 5 * time.Second
	// shortTTL covers denials and anonymous answers: a user who connects an account must not wait long.
	shortTTL     = 3 * time.Second
	maxCacheSize = 4096
)

// Authorizer asks the service whether a request is allowed and which credential to use.
type Authorizer interface {
	Authorize(ctx context.Context, token string, req gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error)
}

// ServiceClient calls the internal listener of the service.
type ServiceClient struct {
	URL  string // base URL, for example http://eduide-service:8083
	HTTP *http.Client
}

// Authorize returns a denial with reason "unauthorized" when the service rejects the instance token, and an
// error when the service cannot answer.
func (c *ServiceClient) Authorize(ctx context.Context, token string, req gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+gitapi.AuthorizePath, bytes.NewReader(body))
	if err != nil {
		return gitapi.AuthorizeResponse{}, err
	}
	hr.Header.Set("Authorization", "Bearer "+token)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return gitapi.AuthorizeResponse{}, fmt.Errorf("authorize call: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonUnauthorized, Message: "EduIDE could not verify this session."}, nil
	case resp.StatusCode != http.StatusOK:
		return gitapi.AuthorizeResponse{}, fmt.Errorf("authorize call: status %d", resp.StatusCode)
	}
	var out gitapi.AuthorizeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return gitapi.AuthorizeResponse{}, fmt.Errorf("authorize call: bad answer")
	}
	return out, nil
}

// cacheKey includes a hash of the token, so a changed token never reuses an answer.
type cacheKey struct {
	instance, host, repo, kind string
	token                      [sha256.Size]byte
}

type cacheEntry struct {
	resp    gitapi.AuthorizeResponse
	expires time.Time
}

// decisionCache keeps authorize answers for a few seconds. It is bounded, so a flood of distinct requests cannot grow it.
type decisionCache struct {
	mu  sync.Mutex
	m   map[cacheKey]cacheEntry
	now func() time.Time
}

func newDecisionCache() *decisionCache {
	return &decisionCache{m: map[cacheKey]cacheEntry{}, now: time.Now}
}

func (c *decisionCache) get(k cacheKey) (gitapi.AuthorizeResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || !c.now().Before(e.expires) {
		delete(c.m, k)
		return gitapi.AuthorizeResponse{}, false
	}
	return e.resp, true
}

func (c *decisionCache) put(k cacheKey, r gitapi.AuthorizeResponse) {
	ttl := shortTTL
	if r.Allow && r.Reason == gitapi.ReasonOK {
		ttl = allowTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= maxCacheSize {
		for key, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= maxCacheSize {
			return
		}
	}
	c.m[k] = cacheEntry{resp: r, expires: now.Add(ttl)}
}

// unwrapURLError drops the request URL from a transport error.
func unwrapURLError(err error) error {
	if ue, ok := err.(interface{ Unwrap() error }); ok && ue.Unwrap() != nil {
		return ue.Unwrap()
	}
	return err
}

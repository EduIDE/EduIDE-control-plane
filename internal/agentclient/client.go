// Package agentclient is the operator's client for the agent control API (api/agent/v1.yaml).
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// ErrConflict means the agent is claimed by another session (HTTP 409).
var ErrConflict = errors.New("agentclient: claimed by another session")

// Client talks to agents. The base URL and token are per call, because every pod has its own.
type Client struct {
	HTTP *http.Client
}

// New returns a client with a short timeout; callers retry. It never follows a redirect: whatever answers on
// the control port of a session pod must not send the operator, and the claim body, to another address.
func New() *Client {
	return &Client{HTTP: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Claim pushes the launch data once. The data is never logged or put into errors.
func (c *Client) Claim(ctx context.Context, base, token string, req agentapi.ClaimRequest) error {
	return c.do(ctx, "claim", http.MethodPost, base+"/v1/claim", token, req, nil)
}

// Status reads the activity of the claimed session.
func (c *Client) Status(ctx context.Context, base, token string) (agentapi.Status, error) {
	var s agentapi.Status
	err := c.do(ctx, "status", http.MethodGet, base+"/v1/status", token, nil, &s)
	return s, err
}

// Notify queues a notice for the user.
func (c *Client) Notify(ctx context.Context, base, token string, n agentapi.Notice) error {
	return c.do(ctx, "notify", http.MethodPost, base+"/v1/notify", token, n, nil)
}

func (c *Client) do(ctx context.Context, op, method, url, token string, in, out any) (err error) {
	defer func() {
		r := "ok"
		if err != nil {
			r = "error"
		}
		telemetry.AgentRequests.WithLabelValues(op, r).Inc()
	}()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("agent %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusConflict:
		return ErrConflict
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("agent %s: HTTP %d", op, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}

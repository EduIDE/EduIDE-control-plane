package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func TestClient(t *testing.T) {
	var got agentapi.ClaimRequest
	last := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/claim":
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got.Session == "other" {
				w.WriteHeader(http.StatusConflict)
			}
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(agentapi.Status{Claimed: true, Ready: true, LastActivity: &last})
		case "/v1/notify":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c, ctx := New(), context.Background()

	if err := c.Claim(ctx, srv.URL, "tok", agentapi.ClaimRequest{Session: "s1", Data: map[string]string{"A": "b"}}); err != nil || got.Data["A"] != "b" {
		t.Fatalf("claim: %v %v", err, got)
	}
	if err := c.Claim(ctx, srv.URL, "tok", agentapi.ClaimRequest{Session: "other"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err := c.Claim(ctx, srv.URL, "bad", agentapi.ClaimRequest{Session: "s1"}); err == nil {
		t.Fatal("want error for wrong token")
	}
	s, err := c.Status(ctx, srv.URL, "tok")
	if err != nil || !s.Ready || !s.LastActivity.Equal(last) {
		t.Fatalf("status: %v %v", err, s)
	}
	if err := c.Notify(ctx, srv.URL, "tok", agentapi.Notice{Kind: agentapi.IdleWarning, Message: "x"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if n := testutil.ToFloat64(telemetry.AgentRequests.WithLabelValues("claim", "error")); n < 2 {
		t.Errorf("error counter %v", n)
	}
	if err := c.Notify(ctx, "http://127.0.0.1:1", "tok", agentapi.Notice{}); err == nil {
		t.Error("want connection error")
	}
}

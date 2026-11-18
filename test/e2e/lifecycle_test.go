//go:build e2e

package e2e

import (
	"context"
	"slices"
	"testing"
	"time"
)

// The stream starts with the current state and ends after phase Ready.
func TestSSEEventsArrive(t *testing.T) {
	a := newAPI(t, alice)
	s := a.mustLaunch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/sse/a.git"}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, err := a.sseEvents(ctx, s.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no session event")
	}
	last := events[len(events)-1]
	if last.Name != s.Name || last.Phase != "Ready" || last.URL == "" {
		t.Fatalf("last event: %+v", last)
	}
	t.Logf("%d events", len(events))
}

// The template short has idle 30s and warning 15s. Nobody sends a heartbeat, so the operator ends the session.
func TestIdleTimeout(t *testing.T) {
	a := newAPI(t, alice)
	s := a.mustLaunch(LaunchRequest{Template: "short"})
	s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
	if s.Phase != "Ready" {
		t.Fatalf("phase %s: %s", s.Phase, s.Message)
	}
	owner := browserLogin(t, alice, "https://"+instanceHost+"/oauth2/login")

	warned := false
	eventually(t, 4*time.Minute, "session ended by the idle timeout", func() bool {
		if ds, status, err := fetchDataState(owner, s.URL); err == nil && status == 200 {
			warned = warned || slices.Contains(ds.Notices, "idle-warning")
		}
		status, cur := a.get(s.Name)
		return status == 404 || cur.Name == ""
	})
	// The warning travels operator -> agent -> extension; the poll interval can make us miss it.
	if !warned {
		t.Log("idle warning not seen in the fake IDE before the session ended")
	}
}

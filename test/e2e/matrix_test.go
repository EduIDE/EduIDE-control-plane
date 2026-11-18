//go:build e2e

package e2e

import (
	"io"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The four cells of ADR 0007: persistence none or volume, with or without a pool.
// A volume session never uses the pool: it starts cold.
func TestMatrix(t *testing.T) {
	cells := []struct {
		name        string
		template    string
		persistence string
		want        string
		warm        bool
	}{
		{"none-without-pool", "plain", "none", "cold", false},
		{"none-with-pool", "warm", "none", "warm-ready", true},
		{"volume-without-pool", "plain", "volume", "cold", false},
		{"volume-with-pool", "warm", "volume", "cold", false},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			a := newAPI(t, alice)
			if c.template == "warm" {
				waitPoolIdle(t, "warm", 2)
			}
			start := time.Now()
			s := a.mustLaunch(LaunchRequest{Template: c.template, Persistence: c.persistence})
			s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
			elapsed := time.Since(start)
			if s.Phase != "Ready" {
				t.Fatalf("phase %s: %s %s", s.Phase, s.ErrorCode, s.Message)
			}
			t.Logf("%s: %s start in %s", c.name, s.StartStrategy, elapsed.Round(10*time.Millisecond))
			if s.StartStrategy != c.want {
				t.Fatalf("startStrategy = %q, want %q", s.StartStrategy, c.want)
			}
			if c.warm && elapsed > 5*time.Second {
				t.Errorf("warm start took %s, budget is 2s p95 (5s hard limit here)", elapsed)
			}

			hc := browserLogin(t, alice, "https://"+instanceHost+"/login/")
			resp, err := hc.Get(s.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 || !strings.Contains(string(b), "fake-ide") {
				t.Fatalf("session URL: status %d", resp.StatusCode)
			}

			if c.persistence == "volume" {
				if s.Workspace == "" {
					t.Fatal("volume session has no workspace")
				}
				var ws v1alpha1.IDEWorkspace
				if err := kube.Get(ctx(), client.ObjectKey{Namespace: namespace, Name: s.Workspace}, &ws); err != nil {
					t.Fatal(err)
				}
				if ws.Status.ClaimName == "" {
					t.Fatal("workspace has no claim")
				}
			} else if s.Workspace != "" {
				t.Fatalf("session without persistence has workspace %q", s.Workspace)
			}
		})
	}
}

// A repeated launch returns the same session with 200; for volume sessions the key is the workspace.
func TestLaunchIsIdempotent(t *testing.T) {
	a := newAPI(t, alice)
	for _, req := range []LaunchRequest{
		{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/idem/none.git"}},
		{Template: "plain", Persistence: "volume"},
	} {
		first := a.mustLaunch(req)
		status, second, _ := a.launch(req)
		if status != 200 || second.Name != first.Name {
			t.Fatalf("second launch of %+v: status %d name %q, want 200 and %q", req, status, second.Name, first.Name)
		}
	}
}

// With 3 running sessions the 4th launch fails and the problem lists the running ones.
func TestSessionLimit(t *testing.T) {
	a := newAPI(t, alice)
	for i := range 3 {
		a.mustLaunch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/limit/" + string(rune('a'+i)) + ".git"}})
	}
	status, _, p := a.launch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/limit/d.git"}})
	if status < 400 || p.Code != "session-limit-reached" || len(p.Sessions) != 3 {
		t.Fatalf("4th launch: status %d code %q sessions %d", status, p.Code, len(p.Sessions))
	}
}

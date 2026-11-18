//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func get(t *testing.T, c *http.Client, u string) (int, string, http.Header) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// ADR 0005: owner 200, other user 403, anonymous redirect to Keycloak, free instance 403, released 403.
func TestGatewayAuth(t *testing.T) {
	a := newAPI(t, alice)
	s := a.mustLaunch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/auth/a.git"}})
	s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
	if s.Phase != "Ready" {
		t.Fatalf("phase %s: %s", s.Phase, s.Message)
	}

	owner := browserLogin(t, alice, "https://"+instanceHost+"/oauth2/login")
	other := browserLogin(t, bob, "https://"+instanceHost+"/oauth2/login")
	anon := testHTTPClient(t, false, false)

	t.Run("owner", func(t *testing.T) {
		status, body, _ := get(t, owner, s.URL)
		if status != 200 || !strings.Contains(body, "fake-ide") {
			t.Fatalf("status %d", status)
		}
	})
	t.Run("other user", func(t *testing.T) {
		if status, _, _ := get(t, other, s.URL); status != 403 {
			t.Fatalf("status %d, want 403", status)
		}
	})
	t.Run("anonymous", func(t *testing.T) {
		status, body, h := get(t, anon, s.URL)
		if status != 302 || !strings.Contains(h.Get("Location"), keycloakHost) || strings.Contains(body, "fake-ide") {
			t.Fatalf("status %d location %q, want redirect to Keycloak and no IDE content", status, h.Get("Location"))
		}
	})
	t.Run("instance token is stripped", func(t *testing.T) {
		status, body, _ := get(t, owner, s.URL+"headers")
		if status != 200 || strings.Contains(strings.ToLower(body), "x-eduide-instance-token") {
			t.Fatalf("status %d body %s", status, body)
		}
	})
	t.Run("websocket", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, wsResp, err := websocket.Dial(ctx, strings.Replace(s.URL, "https://", "wss://", 1)+"ws", &websocket.DialOptions{HTTPClient: owner})
		if wsResp != nil && wsResp.Body != nil {
			_ = wsResp.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.CloseNow() }()
		if err := c.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
			t.Fatal(err)
		}
		if _, msg, err := c.Read(ctx); err != nil || string(msg) != "hello" {
			t.Fatalf("echo %q %v", msg, err)
		}
	})
	t.Run("free pool instance", func(t *testing.T) {
		waitPoolIdle(t, "warm", 1)
		idle := idlePods(t, "warm")
		if len(idle) == 0 {
			t.Fatal("no idle pod")
		}
		u := "https://" + instanceHost + "/" + idle[0].Labels[v1alpha1.LabelInstance] + "/"
		if status, _, _ := get(t, owner, u); status != 403 {
			t.Fatalf("status %d, want 403", status)
		}
	})
	t.Run("after release", func(t *testing.T) {
		if status, _ := a.do(http.MethodDelete, "/sessions/"+s.Name, nil); status != 204 {
			t.Fatalf("delete: %d", status)
		}
		// authz reads an informer: allow a moment, but the end state must be 403 and never the IDE.
		var status int
		eventually(t, 15*time.Second, "denied after release", func() bool {
			status, _, _ = get(t, owner, s.URL)
			return status != 200
		})
		if status != 403 {
			t.Fatalf("status %d, want 403", status)
		}
	})
}

// ADR 0019: a pod in the namespace cannot reach a session pod. The gateway (Envoy) can.
func TestNetworkPolicyIsolatesSessionPods(t *testing.T) {
	a := newAPI(t, alice)
	s := a.mustLaunch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": "https://git.example.org/np/a.git"}})
	s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
	if s.Phase != "Ready" {
		t.Fatalf("phase %s: %s", s.Phase, s.Message)
	}
	pod := podOfSession(t, s)
	ip := pod.Status.PodIP

	// Positive control 1: Envoy reaches the pod, the owner sees the IDE.
	owner := browserLogin(t, alice, "https://"+instanceHost+"/oauth2/login")
	if status, _, _ := get(t, owner, s.URL); status != 200 {
		t.Fatalf("through the gateway: %d", status)
	}

	res := runProbe(t, `for p in 8080 8081 3000; do nc -z -w 3 `+ip+` $p && echo OPEN $p || echo CLOSED $p; done
nc -z -w 3 gateway-entry.envoy-gateway-system.svc 443 && echo OPEN control || echo CLOSED control`)
	// Positive control 2: the probe pod has a working network, so the CLOSED lines mean policy.
	if !strings.Contains(res, "OPEN control") {
		t.Fatalf("probe cannot reach the gateway, the test proves nothing:\n%s", res)
	}
	for _, p := range []string{"8080", "8081", "3000"} {
		if !strings.Contains(res, "CLOSED "+p) {
			t.Errorf("port %s of the session pod is reachable from a foreign pod:\n%s", p, res)
		}
	}
	if t.Failed() {
		t.Log("the CNI does not enforce NetworkPolicies (ADR 0019): do not release this cluster")
	}
}

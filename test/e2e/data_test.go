//go:build e2e

package e2e

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// Launch data reaches the IDE through the agent, and the token appears nowhere else (G16, G18).
func TestDataReachesIDEAndStaysOutOfLogs(t *testing.T) {
	for _, template := range []string{"plain", "warm"} {
		t.Run(template, func(t *testing.T) {
			if template == "warm" {
				waitPoolIdle(t, "warm", 1)
			}
			a := newAPI(t, alice)
			secret := make([]byte, 12)
			_, _ = rand.Read(secret)
			artemisToken := "tok-" + hex.EncodeToString(secret)
			data := map[string]string{
				"GIT_URI":       "https://git.example.org/course/ex1.git",
				"ARTEMIS_URL":   "https://artemis.example.org",
				"ARTEMIS_TOKEN": artemisToken,
			}
			s := a.mustLaunch(LaunchRequest{Template: template, Data: data})
			s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
			if s.Phase != "Ready" {
				t.Fatalf("phase %s: %s", s.Phase, s.Message)
			}

			// GIT_USER and GIT_MAIL come from the token, not from the request.
			tc := claims(t, a.token)
			data["GIT_USER"], _ = tc["name"].(string)
			data["GIT_MAIL"], _ = tc["email"].(string)
			keys := make([]string, 0, len(data))
			for k := range data {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			h := sha256.New()
			for _, k := range keys {
				h.Write([]byte(data[k]))
			}
			wantHash := hex.EncodeToString(h.Sum(nil))

			hc := browserLogin(t, alice, "https://"+instanceHost+"/login/")
			var ds dataState
			eventually(t, 30*time.Second, "fake IDE has the launch data", func() bool {
				var status int
				var err error
				ds, status, err = fetchDataState(hc, s.URL)
				return err == nil && status == 200 && ds.Claimed
			})
			if !slices.Equal(ds.Keys, keys) || ds.SHA256 != wantHash {
				t.Fatalf("data-state keys %v hash %s, want %v hash %s", ds.Keys, ds.SHA256, keys, wantHash)
			}

			// No log of any pod in the namespace and no object of the session contains the token.
			var pods corev1.PodList
			if err := kube.List(ctx(), &pods, client.InNamespace(namespace)); err != nil {
				t.Fatal(err)
			}
			for _, p := range pods.Items {
				l, err := logsOf(ctx(), &p)
				if err != nil {
					t.Logf("logs of %s: %v", p.Name, err)
					continue
				}
				if strings.Contains(l, artemisToken) {
					t.Errorf("token in the logs of pod %s", p.Name)
				}
				raw, _ := json.Marshal(p)
				if strings.Contains(string(raw), artemisToken) {
					t.Errorf("token in the spec of pod %s", p.Name)
				}
			}
			status, body := a.do("GET", "/sessions/"+s.Name, nil)
			if status != 200 || strings.Contains(string(body), artemisToken) {
				t.Errorf("token in the session API answer (status %d)", status)
			}
			var cr v1alpha1.IDESession
			if err := kube.Get(ctx(), client.ObjectKey{Namespace: namespace, Name: s.Name}, &cr); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(cr)
			if strings.Contains(string(raw), artemisToken) {
				t.Error("token in the IDESession")
			}
			t.Logf("checked %d pods", len(pods.Items))
		})
	}
}

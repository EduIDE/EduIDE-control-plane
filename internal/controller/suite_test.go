package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const (
	testNS       = "default"
	nodePoolKey  = "pool.example.org/name"
	testGrace    = 2 * time.Second
	testPollTime = 200 * time.Millisecond
)

var (
	k8s     client.Client
	restCfg *rest.Config
	clk     = &offsetClock{}
	agent   = &fakeAgent{}
)

// offsetClock is the wall clock plus an offset that tests move.
type offsetClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *offsetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *offsetClock) Step(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.off += d
}

// fakeAgent records what the operator sends to agents.
type fakeAgent struct {
	mu      sync.Mutex
	claims  []agentapi.ClaimRequest
	notices []agentapi.Notice
	tokens  []string
	// activity is the lastActivity /v1/status reports; down makes /v1/status fail.
	activity *time.Time
	down     bool
}

func (a *fakeAgent) set(f func(*fakeAgent)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

func (a *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens = append(a.tokens, r.Header.Get("Authorization"))
	switch r.URL.Path {
	case "/v1/claim":
		var c agentapi.ClaimRequest
		_ = json.NewDecoder(r.Body).Decode(&c)
		a.claims = append(a.claims, c)
	case "/v1/status":
		if a.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(agentapi.Status{Claimed: true, Ready: true, LastActivity: a.activity})
	case "/v1/notify":
		var n agentapi.Notice
		_ = json.NewDecoder(r.Body).Decode(&n)
		a.notices = append(a.notices, n)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *fakeAgent) claimFor(session string) *agentapi.ClaimRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.claims {
		if a.claims[i].Session == session {
			return &a.claims[i]
		}
	}
	return nil
}

func (a *fakeAgent) claimCount(session string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for i := range a.claims {
		if a.claims[i].Session == session {
			n++
		}
	}
	return n
}

func (a *fakeAgent) noticeCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.notices)
}

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		println("KUBEBUILDER_ASSETS not set; skipping envtest suite")
		os.Exit(m.Run())
	}
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/gateway-api").Output()
	if err != nil {
		panic(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd"),
			filepath.Join(strings.TrimSpace(string(out)), "config", "crd", "standard", "gateway.networking.k8s.io_httproutes.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	code := runSuite(cfg, m)
	_ = env.Stop()
	os.Exit(code)
}

func runSuite(c *rest.Config, m *testing.M) int {
	restCfg = c
	cfg, err := config.Parse([]byte(`
deployment: test
namespace: ` + testNS + `
hosts: {service: eduide.example.org, instance: ide.example.org}
gateway: {name: gw, namespace: gateway-system, sectionName: https}
oidc: {issuerURL: "https://sso.example.org", clientIDs: [landing], gatewayClientID: gateway}
agent: {image: "example.org/agent:1"}
workspaces: {storageClassName: fast, grace: 2s}
placement: {nodePoolLabel: ` + nodePoolKey + `, prepull: {enabled: true}}
capacity: {exhaustedAfter: 1s}
`))
	if err != nil {
		panic(err)
	}
	ctrl.SetLogger(funcr.New(func(p, a string) {
		if strings.Contains(a, `"error"`) {
			fmt.Println(p, a)
		}
	}, funcr.Options{}))
	srv := httptest.NewServer(agent)
	defer srv.Close()
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{Scheme: NewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		panic(err)
	}
	err = SetupWithManager(mgr, Options{Config: cfg, Clock: clk, PollInterval: testPollTime,
		AgentURL: func(*corev1.Pod) (string, error) { return srv.URL, nil }})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()
	k8s, err = client.New(restCfg, client.Options{Scheme: NewScheme()})
	if err != nil {
		panic(err)
	}
	return m.Run()
}

func requireEnv(t *testing.T) {
	t.Helper()
	if k8s == nil {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newTemplate(name string) *v1.IDETemplate {
	return &v1.IDETemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1.IDETemplateSpec{
			Image: "example.org/ide:1", Port: 3000,
			Env: []v1.EnvVar{{Name: "THEIA", Value: "1"}},
			Persistence: v1.TemplatePersistence{
				Allowed: []v1.PersistenceMode{v1.PersistenceNone, v1.PersistenceVolume}, Default: v1.PersistenceNone, MountPath: "/home/project",
			},
		},
	}
}

func mustCreate(t *testing.T, o client.Object) {
	t.Helper()
	if err := k8s.Create(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func get(o client.Object) error {
	return k8s.Get(context.Background(), client.ObjectKeyFromObject(o), o)
}

func newSession(name, template, owner string) *v1.IDESession {
	return &v1.IDESession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, Labels: map[string]string{v1.LabelManagedBy: v1.LabelManagedByValue}},
		Spec:       v1.IDESessionSpec{Owner: owner, TemplateRef: template, DataSecretRef: name + "-data"},
	}
}

// createSession creates the data Secret and the session, as the service does.
func createSession(t *testing.T, name, template string, mutate ...func(*v1.IDESession)) *v1.IDESession {
	t.Helper()
	s := newSession(name, template, "alice")
	for _, f := range mutate {
		f(s)
	}
	mustCreate(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: s.Spec.DataSecretRef, Namespace: testNS, Labels: map[string]string{v1.LabelManagedBy: v1.LabelManagedByValue}},
		StringData: map[string]string{"GIT_URI": "https://git.example.org/r.git"},
	})
	mustCreate(t, s)
	return s
}

func sessionStatus(name string) v1.IDESessionStatus {
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}}
	_ = get(s)
	return s.Status
}

func podsWith(l client.MatchingLabels) []corev1.Pod {
	var pl corev1.PodList
	_ = k8s.List(context.Background(), &pl, client.InNamespace(testNS), l)
	var out []corev1.Pod
	for _, p := range pl.Items {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out
}

func setReady(t *testing.T, name string) {
	t.Helper()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	p.Status.PodIP = "10.0.0.1"
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	if err := k8s.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func setUnschedulable(t *testing.T, name string) {
	t.Helper()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, LastTransitionTime: metav1.Now()}}
	if err := k8s.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func notFound(o client.Object) bool { return apierrors.IsNotFound(get(o)) }

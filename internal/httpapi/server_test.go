package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const gitURI = "https://git.example.org/course/ex01-ab12cde.git"

func launchBody(extra map[string]any) map[string]any {
	b := map[string]any{"template": "java", "data": map[string]string{"GIT_URI": gitURI, "ARTEMIS_TOKEN": "secret-token-value"}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func problemCode(t *testing.T, e *env, method, path, token string, body any, wantStatus int) Problem {
	t.Helper()
	rec := e.do(method, path, token, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	return decode[Problem](t, rec)
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "nobody"} {
		if p := problemCode(t, e, "GET", "/sessions", tok, nil, 401); p.Code != CodeUnauthorized {
			t.Errorf("code %s", p.Code)
		}
	}
}

func TestLaunch(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/sessions", "alice", launchBody(nil), "traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	s := decode[Session](t, rec)
	if !strings.HasPrefix(s.Name, "s-") || len(s.Name) != 12 || s.Phase != "Pending" {
		t.Fatalf("session %+v", s)
	}
	if strings.Contains(rec.Body.String(), "secret-token-value") {
		t.Fatal("launch data in the response")
	}

	var cr v1alpha1.IDESession
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: s.Name}, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Spec.Owner != "alice" || cr.Spec.DataSecretRef != s.Name+"-data" || cr.Spec.WorkspaceRef != "" {
		t.Fatalf("spec %+v", cr.Spec)
	}
	if cr.Annotations[v1alpha1.AnnotationTraceparent] == "" || cr.Labels[v1alpha1.LabelManagedBy] != "eduide" || len(cr.Labels[v1alpha1.LabelLaunchKey]) != 40 {
		t.Fatalf("metadata %+v %+v", cr.Labels, cr.Annotations)
	}
	if cr.Spec.Display.Source != gitURI {
		t.Fatalf("display %+v", cr.Spec.Display)
	}
	if b, _ := json.Marshal(cr); strings.Contains(string(b), "secret-token-value") {
		t.Fatal("launch data in the CR")
	}

	var sec corev1.Secret
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: s.Name + "-data"}, &sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data["ARTEMIS_TOKEN"]) != "secret-token-value" || string(sec.Data["GIT_USER"]) != "Alice A" || string(sec.Data["GIT_MAIL"]) != "alice@example.org" {
		t.Fatalf("secret keys %v", keys(sec.Data))
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != s.Name || sec.Labels[v1alpha1.LabelManagedBy] != "eduide" {
		t.Fatalf("secret metadata %+v", sec.ObjectMeta)
	}

	// Same launch again returns the same session.
	rec = e.do("POST", "/sessions", "alice", launchBody(nil))
	if rec.Code != 200 || decode[Session](t, rec).Name != s.Name {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body)
	}
	// Another user gets a session of their own.
	rec = e.do("POST", "/sessions", "bob", launchBody(nil))
	if rec.Code != 201 || decode[Session](t, rec).Name == s.Name {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLaunchErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   any
		status int
		code   ProblemCode
	}{
		{"unknown template", map[string]any{"template": "nope"}, 404, CodeTemplateNotFound},
		{"unknown key", launchBody(map[string]any{"data": map[string]string{"GIT_URI": gitURI, "FROM": "x"}}), 400, CodeUnknownKey},
		{"bad url", launchBody(map[string]any{"data": map[string]string{"GIT_URI": "http://git.example.org/a"}}), 400, CodeInvalidInput},
		{"too large", launchBody(map[string]any{"data": map[string]string{"GIT_URI": gitURI, "ARTEMIS_TOKEN": strings.Repeat("a", 8100), "BUILD": strings.Repeat("a", 200)}}), 413, CodePayloadTooLarge},
		{"extra field", launchBody(map[string]any{"fromSecrets": []string{"x"}}), 400, CodeInvalidInput},
		{"missing workspace", launchBody(map[string]any{"workspace": "w-none"}), 404, CodeWorkspaceNotFound},
		{"credentials in display", launchBody(map[string]any{"display": map[string]string{"source": "https://u:p@git.example.org/a"}}), 400, CodeInvalidInput},
		{"script in display", launchBody(map[string]any{"display": map[string]string{"source": "javascript:alert(1)"}}), 400, CodeInvalidInput},
		{"padded script in display", launchBody(map[string]any{"display": map[string]string{"source": " javascript:alert(1)"}}), 400, CodeInvalidInput},
	}
	e := newEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if p := problemCode(t, e, "POST", "/sessions", "alice", tt.body, tt.status); p.Code != tt.code {
				t.Fatalf("code %s, want %s", p.Code, tt.code)
			}
		})
	}
	// Nothing was created by the rejected launches.
	var l v1alpha1.IDESessionList
	_ = e.k.List(context.Background(), &l)
	if len(l.Items) != 0 {
		t.Fatalf("%d sessions created", len(l.Items))
	}
}

func TestLaunchPersistence(t *testing.T) {
	e := newEnv(t)
	noVolume := newEnv(t)
	var cur v1alpha1.IDETemplate
	_ = noVolume.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "java"}, &cur)
	cur.Spec.Persistence.Allowed = []v1alpha1.PersistenceMode{"none"}
	if err := noVolume.k.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	if p := problemCode(t, noVolume, "POST", "/sessions", "alice", launchBody(map[string]any{"persistence": "volume"}), 400); p.Code != CodePersistence {
		t.Fatalf("code %s", p.Code)
	}

	// Volume without a workspace: get-or-create with the deterministic name.
	rec := e.do("POST", "/sessions", "alice", launchBody(map[string]any{"persistence": "volume"}))
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s := decode[Session](t, rec)
	if s.Workspace == nil || *s.Workspace != workspaceName("alice", "java", gitURI) {
		t.Fatalf("workspace %v", s.Workspace)
	}
	var ws v1alpha1.IDEWorkspace
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: *s.Workspace}, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.Spec.Owner != "alice" || ws.Spec.Persistence.Mode != "volume" || ws.Labels[v1alpha1.LabelManagedBy] != "eduide" {
		t.Fatalf("workspace %+v", ws)
	}
	ownedByWorkspace := func(name string) {
		t.Helper()
		var cr v1alpha1.IDESession
		if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: name}, &cr); err != nil {
			t.Fatal(err)
		}
		// U14: the garbage collector removes a session whose workspace was deleted during its launch.
		if refs := cr.OwnerReferences; len(refs) != 1 || refs[0].Kind != "IDEWorkspace" || refs[0].Name != ws.Name || refs[0].UID != ws.UID {
			t.Fatalf("owner references %+v", refs)
		}
	}
	ownedByWorkspace(s.Name)
	// A repeat finds the session; stopping and launching again reuses the workspace.
	if rec := e.do("POST", "/sessions", "alice", launchBody(map[string]any{"persistence": "volume"})); rec.Code != 200 {
		t.Fatalf("repeat %d", rec.Code)
	}
	if rec := e.do("DELETE", "/sessions/"+s.Name, "alice", nil); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}

	// Explicit workspace of another user is not found, own workspace is used.
	if p := problemCode(t, e, "POST", "/sessions", "bob", launchBody(map[string]any{"workspace": *s.Workspace}), 404); p.Code != CodeWorkspaceNotFound {
		t.Fatalf("code %s", p.Code)
	}
	rec = e.do("POST", "/sessions", "alice", map[string]any{"template": "java", "workspace": *s.Workspace, "data": map[string]string{"GIT_URI": gitURI}})
	if rec.Code != 201 || *decode[Session](t, rec).Workspace != *s.Workspace {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ownedByWorkspace(decode[Session](t, rec).Name)
	if p := problemCode(t, e, "POST", "/sessions", "alice", map[string]any{"template": "java", "workspace": *s.Workspace, "persistence": "none"}, 400); p.Code != CodeInvalidInput {
		t.Fatalf("code %s", p.Code)
	}
	// A workspace whose volume is lost refuses new sessions (an existing one with the same launch key is not new).
	var running v1alpha1.IDESessionList
	_ = e.k.List(context.Background(), &running)
	for i := range running.Items {
		_ = e.k.Delete(context.Background(), &running.Items[i])
	}
	var lost v1alpha1.IDEWorkspace
	_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: *s.Workspace}, &lost)
	lost.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: v1alpha1.ReasonVolumeLost, LastTransitionTime: metav1.Now()}}
	if err := e.k.Update(context.Background(), &lost); err != nil {
		t.Fatal(err)
	}
	if p := problemCode(t, e, "POST", "/sessions", "alice", launchBody(map[string]any{"workspace": *s.Workspace}), 409); p.Code != CodeWorkspaceLost {
		t.Fatalf("code %s", p.Code)
	}
}

func TestSessionLimitAndRate(t *testing.T) {
	e := newEnv(t)
	e.srv.cfg.SessionsPerUser = 2
	for i, build := range []string{"maven", "gradle"} {
		if rec := e.do("POST", "/sessions", "alice", launchBody(map[string]any{"data": map[string]string{"GIT_URI": gitURI, "BUILD": build}})); rec.Code != 201 {
			t.Fatalf("launch %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	p := problemCode(t, e, "POST", "/sessions", "alice", launchBody(nil), 409)
	if p.Code != CodeSessionLimit || p.Sessions == nil || len(*p.Sessions) != 2 {
		t.Fatalf("%+v", p)
	}
	// A failed session does not count.
	var l v1alpha1.IDESessionList
	_ = e.k.List(context.Background(), &l)
	l.Items[0].Status.Phase = v1alpha1.SessionFailed
	_ = e.k.Update(context.Background(), &l.Items[0])
	if rec := e.do("POST", "/sessions", "alice", launchBody(nil)); rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	e.srv.limiters = map[string]*rate.Limiter{}
	e.srv.cfg.RateLimit.LaunchesPerMinute = 2
	for range 2 {
		e.do("POST", "/sessions", "bob", map[string]any{"template": "nope"})
	}
	rec := e.do("POST", "/sessions", "bob", map[string]any{"template": "nope"})
	if rec.Code != 429 || decode[Problem](t, rec).Code != CodeRateLimited || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestOwnerIsolation(t *testing.T) {
	e := newEnv(t, session("s-a", "alice", "java", "Ready"), workspace("w-a", "alice", "java"), workspace("w-b", "bob", "java"))
	for _, c := range []struct{ method, path string }{
		{"GET", "/sessions/s-a"}, {"DELETE", "/sessions/s-a"}, {"DELETE", "/workspaces/w-a"}, {"GET", "/sessions/s-a/events"},
	} {
		rec := e.do(c.method, c.path, "bob", nil)
		if rec.Code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := e.do("GET", "/sessions", "bob", nil); strings.Contains(rec.Body.String(), "s-a") {
		t.Fatal("list leaks sessions")
	}
	if rec := e.do("GET", "/workspaces", "bob", nil); strings.Contains(rec.Body.String(), "w-a") || !strings.Contains(rec.Body.String(), "w-b") {
		t.Fatalf("workspace list: %s", rec.Body)
	}
	if rec := e.do("GET", "/sessions/s-a", "alice", nil); rec.Code != 200 {
		t.Fatalf("owner get: %d", rec.Code)
	}
}

func TestDeleteWorkspaceStopsTheSession(t *testing.T) {
	s := session("s-a", "alice", "java", "Ready")
	s.Spec.WorkspaceRef = "w-a"
	e := newEnv(t, s, workspace("w-a", "alice", "java"))
	if rec := e.do("DELETE", "/workspaces/w-a", "alice", nil); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "s-a"}, &v1alpha1.IDESession{}); err == nil {
		t.Error("session still exists")
	}
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "w-a"}, &v1alpha1.IDEWorkspace{}); err == nil {
		t.Error("workspace still exists")
	}
}

func TestDeleteWorkspaceWaitsForTheSession(t *testing.T) {
	s := session("s-a", "alice", "java", "Ready")
	s.Spec.WorkspaceRef = "w-a"
	s.Finalizers = []string{"test/hold"}
	e := newEnv(t, s, workspace("w-a", "alice", "java"))
	e.srv.stopPoll = 10 * time.Millisecond
	// The operator releases the session a moment later.
	go func() {
		time.Sleep(100 * time.Millisecond)
		var cur v1alpha1.IDESession
		_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "s-a"}, &cur)
		cur.Finalizers = nil
		_ = e.k.Update(context.Background(), &cur)
	}()
	if rec := e.do("DELETE", "/workspaces/w-a", "alice", nil); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDeleteWorkspaceTimesOut(t *testing.T) {
	s := session("s-a", "alice", "java", "Ready")
	s.Spec.WorkspaceRef = "w-a"
	s.Finalizers = []string{"test/hold"}
	e := newEnv(t, s, workspace("w-a", "alice", "java"))
	e.srv.stopWait, e.srv.stopPoll = 50*time.Millisecond, 10*time.Millisecond
	problemCode(t, e, "DELETE", "/workspaces/w-a", "alice", nil, 500)
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "w-a"}, &v1alpha1.IDEWorkspace{}); err != nil {
		t.Error("workspace must stay when the session does not stop")
	}
}

func TestCreateWorkspace(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"template": "java", "display": map[string]string{"source": gitURI, "title": "Ex 1"}}
	rec := e.do("POST", "/workspaces", "alice", body)
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	w1 := decode[Workspace](t, rec)
	if w1.Name != workspaceName("alice", "java", gitURI) {
		t.Fatalf("name %s", w1.Name)
	}
	if w2 := decode[Workspace](t, e.do("POST", "/workspaces", "alice", body)); w2.Name != w1.Name {
		t.Fatal("create is not get-or-create")
	}
	if p := problemCode(t, e, "POST", "/workspaces", "alice", map[string]any{"template": "nope"}, 404); p.Code != CodeTemplateNotFound {
		t.Fatalf("code %s", p.Code)
	}
	// Workspace creation shares the launch rate limit.
	e.srv.cfg.RateLimit.LaunchesPerMinute = 1
	e.srv.limiters = map[string]*rate.Limiter{}
	e.do("POST", "/workspaces", "bob", body)
	if p := problemCode(t, e, "POST", "/workspaces", "bob", body, 429); p.Code != CodeRateLimited {
		t.Fatalf("code %s", p.Code)
	}
}

func TestTemplates(t *testing.T) {
	e := newEnv(t, &v1alpha1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: testNS}, Spec: v1alpha1.IDEPoolSpec{TemplateRef: "java"}})
	ts := decode[[]Template](t, e.do("GET", "/templates", "alice", nil))
	if len(ts) != 1 || ts[0].Warm == nil || !*ts[0].Warm || ts[0].DisplayName != "Java" {
		t.Fatalf("%+v", ts)
	}
	if got := strings.Join(ts[0].InjectableKeys, ","); got != "GIT_URI,ARTEMIS_TOKEN,BUILD" {
		t.Fatalf("keys %s (token-sourced keys must not be listed)", got)
	}
}

func TestOverview(t *testing.T) {
	running := session("s-run", "alice", "java", "Ready")
	running.Spec.Display = v1alpha1.Display{Source: "https://git.example.org/c/ex01-ab12cde.git"}
	running.Spec.WorkspaceRef = "w-1"
	titled := session("s-titled", "alice", "java", "Starting")
	titled.Spec.Display = v1alpha1.Display{Title: "My title", Source: "https://git.example.org/c/other"}
	deleting := session("s-del", "alice", "java", "Ready")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}
	w1 := workspace("w-1", "alice", "java")
	w2 := workspace("w-2", "alice", "java")
	w2.Spec.Display = v1alpha1.Display{Source: "https://git.example.org/c/ex02-ab12cde.git"}
	e := newEnv(t, running, titled, deleting, w1, w2, session("s-bob", "bob", "java", "Ready"))

	o := decode[Overview](t, e.do("GET", "/me/overview", "alice", nil))
	got := map[string]OverviewItem{}
	for _, it := range o.Items {
		got[it.Title] = it
	}
	if len(o.Items) != 3 {
		t.Fatalf("%d items: %+v", len(o.Items), o.Items)
	}
	if it := got["ex01"]; !it.Running || it.Kind != "session" || it.Workspace == nil || *it.TemplateDisplayName != "Java" {
		t.Errorf("running item %+v", it)
	}
	if it := got["My title"]; !it.Running {
		t.Errorf("titled item %+v", it)
	}
	if it := got["ex02"]; it.Running || it.Kind != "workspace" {
		t.Errorf("workspace item %+v", it)
	}
	if !o.Items[0].Running || !o.Items[1].Running || o.Items[2].Running {
		t.Errorf("running items must come first: %+v", o.Items)
	}
}

func TestMetricsOperationID(t *testing.T) {
	for pattern, want := range map[string]string{
		"GET /api/v1/sessions/{name}":         "getSession",
		"POST /api/v1/sessions":               "launchSession",
		"GET /api/v1/me/overview/events":      "watchOverview",
		"DELETE /api/v1/workspaces/{name}":    "deleteWorkspace",
		"GET /somewhere/else":                 "unknown",
		http.MethodGet + " /api/v1/templates": "listTemplates",
	} {
		if got := operationID(pattern); got != want {
			t.Errorf("%s: %s, want %s", pattern, got, want)
		}
	}
}

func TestUnknownRoutesAnswerProblemJSON(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/nope", 404},
		{"GET", "/sessions/a/b/c", 404},
		{"PUT", "/sessions", 405},
	} {
		req := httptest.NewRequest(tc.method, basePath+tc.path, nil)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s %s: %d %q %s", tc.method, tc.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
			continue
		}
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != tc.status || p.Code == "" {
			t.Errorf("%s %s: body %s", tc.method, tc.path, rec.Body)
		}
		if tc.status == 405 && rec.Header().Get("Allow") == "" {
			t.Error("Allow header lost")
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/elsewhere", nil))
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("outside the base path: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestResponsesCarryHSTS(t *testing.T) {
	e := newEnv(t)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"unauthorized": e.do("GET", "/sessions", "", nil),
		"ok":           e.do("GET", "/sessions", "alice", nil),
		"unknown": func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			e.handler.ServeHTTP(rec, httptest.NewRequest("GET", basePath+"/nope", nil))
			return rec
		}(),
	} {
		if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
			t.Errorf("%s: HSTS %q", name, got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", name)
		}
	}
}

func TestLaunchIsLoggedWithoutLaunchData(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	e := newEnv(t)
	if rec := e.do("POST", "/sessions", "alice", launchBody(nil)); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	e.do("POST", "/sessions", "alice", map[string]any{"template": "nope"})
	out := buf.String()
	for _, want := range []string{`"msg":"launch"`, `"result":"created"`, `"template":"java"`, `"result":"rejected"`, `"code":"template-not-found"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"alice", "secret-token-value", gitURI} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q", bad)
		}
	}
}

func TestHSTSSubdomainsOptOut(t *testing.T) {
	off := false
	if got := hstsValue(&config.Config{Security: config.Security{HSTSIncludeSubdomains: &off}}); got != "max-age=31536000" {
		t.Errorf("opt-out: %q", got)
	}
}

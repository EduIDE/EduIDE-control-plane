package gitpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const (
	ns          = "eduide"
	instanceTok = "instance-token-1"
	ghToken     = "gho_upstream_secret_token"
	glToken     = "glpat_upstream_secret_token"
)

type fakeSessions []v1alpha1.IDESession

func (f fakeSessions) ByInstance(_ context.Context, instance string) ([]v1alpha1.IDESession, error) {
	var out []v1alpha1.IDESession
	for _, s := range f {
		if s.Status.Instance == instance {
			out = append(out, s)
		}
	}
	return out, nil
}

type rig struct {
	h *Handler
	k client.Client
}

func session(name, owner, instance, dataSecret string, phase v1alpha1.SessionPhase) v1alpha1.IDESession {
	return v1alpha1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:   v1alpha1.IDESessionSpec{Owner: owner, TemplateRef: "t", DataSecretRef: dataSecret},
		Status: v1alpha1.IDESessionStatus{Phase: phase, Instance: instance}}
}

func newRig(t *testing.T, sessions ...v1alpha1.IDESession) *rig {
	t.Helper()
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)
	k := fake.NewClientBuilder().WithScheme(sch).Build()
	keys, err := gitaccount.NewKeys([]byte(strings.Repeat("k", 40)))
	if err != nil {
		t.Fatal(err)
	}
	provs := []config.GitProvider{
		{Name: "github", Type: "github", Host: "github.com", DisplayName: "GitHub", InactivityExpiry: &metav1.Duration{Duration: time.Hour}},
		{Name: "gitlab", Type: "gitlab", Host: "gitlab.example.org", DisplayName: "Example GitLab", InactivityExpiry: &metav1.Duration{Duration: time.Hour}},
	}
	var entries []gitaccount.Entry
	for _, p := range provs {
		entries = append(entries, gitaccount.Entry{Config: p})
	}
	m := gitaccount.NewManager(k, ns, keys, entries)
	ctx := context.Background()
	if err := m.Save(ctx, "alice", "github", gitaccount.Token{Access: ghToken}, gitaccount.Account{Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(ctx, "alice", "gitlab", gitaccount.Token{Access: glToken}, gitaccount.Account{Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, data map[string]string) {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		if err := k.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	mk("ide-inst1-token", map[string]string{v1alpha1.InstanceTokenKey: instanceTok})
	mk("data-alice", map[string]string{"GIT_URI": "https://x:vcs-token@github.com/Owner/Repo.git"})
	mk("data-alice-gitlab", map[string]string{"GIT_URI": "https://gitlab.example.org/group/sub/proj"})
	mk("data-nogit", map[string]string{"OTHER": "x"})
	return &rig{k: k, h: &Handler{Sessions: fakeSessions(sessions), Secrets: k, Accounts: m, Namespace: ns, LandingURL: "https://ide.example.org", Salt: []byte("salt")}}
}

func (r *rig) call(token string, req gitapi.AuthorizeRequest) (int, gitapi.AuthorizeResponse, string) {
	b, _ := json.Marshal(req)
	hr := httptest.NewRequest(http.MethodPost, gitapi.AuthorizePath, bytes.NewReader(b))
	if token != "" {
		hr.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, hr)
	var out gitapi.AuthorizeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestAuthorize(t *testing.T) {
	ready := func(owner, data string) []v1alpha1.IDESession {
		return []v1alpha1.IDESession{session("s", owner, "inst1", data, v1alpha1.SessionReady)}
	}
	del := session("s-del", "alice", "inst1", "data-alice", v1alpha1.SessionReady)
	now := metav1.Now()
	del.DeletionTimestamp, del.Finalizers = &now, []string{"x"}
	req := func(host, repo, kind string) gitapi.AuthorizeRequest {
		return gitapi.AuthorizeRequest{Instance: "inst1", Host: host, Repository: repo, Kind: kind}
	}
	tests := []struct {
		name     string
		sessions []v1alpha1.IDESession
		token    string
		req      gitapi.AuthorizeRequest
		code     int
		allow    bool
		reason   string
		upstream string // "user:password" or ""
		msgHas   string
	}{
		{"missing token", nil, "", req("github.com", "o/r", "read"), 401, false, "", "", ""},
		{"wrong token", ready("alice", ""), "nope", req("github.com", "o/r", "read"), 401, false, "", "", ""},
		{"unknown instance", nil, instanceTok, gitapi.AuthorizeRequest{Instance: "other", Host: "github.com", Repository: "o/r", Kind: "read"}, 401, false, "", "", ""},
		{"bad kind", nil, instanceTok, req("github.com", "o/r", "delete"), 400, false, "", "", ""},
		{"bad instance name", nil, instanceTok, gitapi.AuthorizeRequest{Instance: "../x", Host: "github.com", Repository: "o/r", Kind: "read"}, 400, false, "", "", ""},
		{"idle instance without session", nil, instanceTok, req("github.com", "o/r", "read"), 200, false, "not-ready", "", ""},
		{"session starting", []v1alpha1.IDESession{session("s", "alice", "inst1", "", v1alpha1.SessionStarting)}, instanceTok, req("github.com", "o/r", "read"), 200, false, "not-ready", "", ""},
		{"session deleting", []v1alpha1.IDESession{del}, instanceTok, req("github.com", "o/r", "read"), 200, false, "not-ready", "", ""},
		{"unknown host", ready("alice", ""), instanceTok, req("evil.example.com", "o/r", "read"), 200, false, "unknown-host", "", "not a Git host"},
		{"read session repository with account", ready("alice", "data-alice"), instanceTok, req("github.com", "Owner/Repo", "read"), 200, true, "ok", "x-access-token:" + ghToken, ""},
		{"read gitlab session repository", ready("alice", "data-alice-gitlab"), instanceTok, req("gitlab.example.org", "group/sub/proj", "read"), 200, true, "ok", "oauth2:" + glToken, ""},
		{"read host case", ready("alice", "data-alice"), instanceTok, req("GitHub.COM", "Owner/Repo.git", "read"), 200, true, "ok", "x-access-token:" + ghToken, ""},
		{"read another repository is anonymous even with an account", ready("alice", "data-alice"), instanceTok, req("github.com", "o/r", "read"), 200, true, "anonymous-other", "", ""},
		{"read without GIT_URI is anonymous even with an account", ready("alice", "data-nogit"), instanceTok, req("github.com", "Owner/Repo", "read"), 200, true, "anonymous-other", "", ""},
		{"read same path on another host is anonymous", ready("alice", "data-alice"), instanceTok, req("gitlab.example.org", "Owner/Repo", "read"), 200, true, "anonymous-other", "", ""},
		{"read session repository without account: another owner's account is never used", ready("bob", "data-alice"), instanceTok, req("github.com", "Owner/Repo", "read"), 200, true, "anonymous", "", ""},
		{"push without account", ready("bob", "data-alice"), instanceTok, req("github.com", "Owner/Repo", "push"), 200, false, "connect", "", "Connect your GitHub account"},
		{"push to session repository", ready("alice", "data-alice"), instanceTok, req("github.com", "Owner/Repo", "push"), 200, true, "ok", "x-access-token:" + ghToken, ""},
		{"push with .git and slash", ready("alice", "data-alice"), instanceTok, req("github.com", "/Owner/Repo.git/", "push"), 200, true, "ok", "x-access-token:" + ghToken, ""},
		{"push host case", ready("alice", "data-alice"), instanceTok, req("GITHUB.com", "Owner/Repo", "push"), 200, true, "ok", "x-access-token:" + ghToken, ""},
		{"push to gitlab session repository", ready("alice", "data-alice-gitlab"), instanceTok, req("gitlab.example.org", "group/sub/proj", "push"), 200, true, "ok", "oauth2:" + glToken, ""},
		{"push elsewhere", ready("alice", "data-alice"), instanceTok, req("github.com", "Owner/Other", "push"), 200, false, "push-rule", "", "github.com/Owner/Repo"},
		{"push same path on another host", ready("alice", "data-alice"), instanceTok, req("gitlab.example.org", "Owner/Repo", "push"), 200, false, "push-rule", "", "github.com/Owner/Repo"},
		{"push without GIT_URI", ready("alice", "data-nogit"), instanceTok, req("github.com", "Owner/Repo", "push"), 200, false, "push-rule", "", "(none)"},
		{"push without data secret", ready("alice", ""), instanceTok, req("github.com", "Owner/Repo", "push"), 200, false, "push-rule", "", "(none)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.sessions...)
			code, resp, raw := r.call(tc.token, tc.req)
			if code != tc.code {
				t.Fatalf("status %d, want %d (%s)", code, tc.code, raw)
			}
			if code != 200 {
				return
			}
			if resp.Allow != tc.allow || resp.Reason != tc.reason {
				t.Fatalf("allow=%v reason=%q, want %v %q (%s)", resp.Allow, resp.Reason, tc.allow, tc.reason, raw)
			}
			got := ""
			if resp.Upstream != nil {
				got = resp.Upstream.Username + ":" + resp.Upstream.Password
			}
			if got != tc.upstream {
				t.Errorf("upstream %q, want %q", got, tc.upstream)
			}
			if tc.msgHas != "" && !strings.Contains(resp.Message, tc.msgHas) {
				t.Errorf("message %q lacks %q", resp.Message, tc.msgHas)
			}
			if !resp.Allow && (strings.Contains(raw, ghToken) || strings.Contains(raw, "vcs-token")) {
				t.Error("a denial carries a secret")
			}
			if len(tc.sessions) > 0 && resp.Reason != "not-ready" && resp.User != telemetry.UserID([]byte("salt"), tc.sessions[0].Spec.Owner) {
				t.Errorf("user %q", resp.User)
			}
		})
	}
}

func TestAuthorizeReportsAccountAndSessionRepository(t *testing.T) {
	read := func(owner, repo string) gitapi.AuthorizeResponse {
		r := newRig(t, session("s", owner, "inst1", "data-alice", v1alpha1.SessionReady))
		_, resp, _ := r.call(instanceTok, gitapi.AuthorizeRequest{Instance: "inst1", Host: "github.com", Repository: repo, Kind: "read"})
		return resp
	}
	if r := read("alice", "o/r"); !r.Connected || r.SessionRepository != "github.com/Owner/Repo" || r.Upstream != nil {
		t.Errorf("other repository with account: %+v", r)
	}
	if r := read("bob", "o/r"); r.Connected || r.SessionRepository != "github.com/Owner/Repo" {
		t.Errorf("other repository without account: %+v", r)
	}
}

func TestAuthorizeMarksAccountUsed(t *testing.T) {
	r := newRig(t, session("s", "alice", "inst1", "data-alice", v1alpha1.SessionReady))
	sec := &corev1.Secret{}
	name := client.ObjectKey{Namespace: ns, Name: gitaccount.SecretName("alice", "github")}
	// Make the last use old, so the next use writes it again.
	if err := r.k.Get(context.Background(), name, sec); err != nil {
		t.Fatal(err)
	}
	sec.Annotations[v1alpha1.AnnotationGitLastUsed] = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if err := r.k.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	if code, resp, _ := r.call(instanceTok, gitapi.AuthorizeRequest{Instance: "inst1", Host: "github.com", Repository: "Owner/Repo", Kind: "read"}); code != 200 || !resp.Allow {
		t.Fatal(code, resp)
	}
	if err := r.k.Get(context.Background(), name, sec); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, sec.Annotations[v1alpha1.AnnotationGitLastUsed])
	if err != nil || time.Since(at) > time.Minute {
		t.Errorf("last used %v %v", at, err)
	}
}

func TestRateLimitPerInstance(t *testing.T) {
	r := newRig(t, session("s", "alice", "inst1", "", v1alpha1.SessionReady))
	req := gitapi.AuthorizeRequest{Instance: "inst1", Host: "github.com", Repository: "o/r", Kind: "read"}
	limited := false
	for range perInstanceBurst + 5 {
		if code, _, _ := r.call(instanceTok, req); code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Error("no rate limit")
	}
	// Another instance has its own budget.
	other := gitapi.AuthorizeRequest{Instance: "inst2", Host: "github.com", Repository: "o/r", Kind: "read"}
	if code, _, _ := r.call(instanceTok, other); code == http.StatusTooManyRequests {
		t.Error("instances share a budget")
	}
}

func TestOnlyPostOnItsPath(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct{ method, path string }{{"GET", gitapi.AuthorizePath}, {"POST", "/other"}} {
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", tc.method, tc.path, rec.Code)
		}
	}
}

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const testNS = "eduide"

type fakeVerifier map[string]auth.User

func (f fakeVerifier) Verify(_ context.Context, raw string) (auth.User, error) {
	if u, ok := f[raw]; ok {
		return u, nil
	}
	return auth.User{}, context.Canceled
}

var (
	alice = auth.User{Owner: "alice", Name: "Alice A", Email: "alice@example.org"}
	bob   = auth.User{Owner: "bob", Name: "Bob B", Email: "bob@example.org"}
)

var verifier = fakeVerifier{"alice": alice, "bob": bob}

func testConfig() *config.Config {
	return &config.Config{
		Namespace: testNS, SessionsPerUser: 3, AllowedHosts: []string{"git.example.org"},
		RateLimit:  config.RateLimit{LaunchesPerMinute: 100},
		Workspaces: config.Workspaces{Grace: &metav1.Duration{Duration: 24 * time.Hour}},
		Overview:   config.Overview{TitleStripPattern: `-[a-z]{2}[0-9]{2}[a-z]{3}$`},
	}
}

func javaTemplate() *v1alpha1.IDETemplate {
	return &v1alpha1.IDETemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "java", Namespace: testNS},
		Spec: v1alpha1.IDETemplateSpec{
			DisplayName: "Java", LogoURL: "https://example.org/java.svg", Image: "ide:1",
			Persistence: v1alpha1.TemplatePersistence{Allowed: []v1alpha1.PersistenceMode{"none", "volume"}, Default: "none"},
			InjectableKeys: []v1alpha1.InjectableKey{
				{Name: "GIT_URI", Format: v1alpha1.FormatHTTPSURL, Required: true},
				{Name: "ARTEMIS_TOKEN", Format: v1alpha1.FormatToken, MaxLength: 64},
				{Name: "BUILD", Format: v1alpha1.FormatEnum, Values: []string{"maven", "gradle"}},
				{Name: "GIT_USER", Source: v1alpha1.SourceUserName},
				{Name: "GIT_MAIL", Source: v1alpha1.SourceUserEmail},
			},
		},
	}
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

type env struct {
	t       *testing.T
	srv     *Server
	handler http.Handler
	k       client.Client
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	k := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(append([]client.Object{javaTemplate()}, objs...)...).Build()
	srv := NewServer(testConfig(), k, k, NewHub(), []byte("salt"))
	return &env{t: t, srv: srv, handler: srv.Handler(verifier), k: k}
}

// do sends a request and checks the response against the OpenAPI spec (contract test).
func (e *env) do(method, path, token string, body any, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if s, ok := body.(string); ok {
		rd = bytes.NewReader([]byte(s))
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, basePath+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	validateResponse(e.t, req, rec)
	return rec
}

func validateResponse(t *testing.T, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	spec, err := GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil // the spec server is a relative base path; routes are matched on the full path
	for p, item := range spec.Paths.Map() {
		spec.Paths.Set(basePath+p, item)
		spec.Paths.Delete(p)
	}
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("no route in spec for %s %s: %v", req.Method, req.URL.Path, err)
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	in.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		t.Errorf("response %d of %s %s violates the OpenAPI spec: %v\nbody: %s", rec.Code, req.Method, req.URL.Path, err, rec.Body.String())
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func session(name, owner, template, phase string) *v1alpha1.IDESession {
	return &v1alpha1.IDESession{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNS, CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			Labels: managedLabels(owner, map[string]string{v1alpha1.LabelTemplate: template}),
		},
		Spec:   v1alpha1.IDESessionSpec{Owner: owner, TemplateRef: template},
		Status: v1alpha1.IDESessionStatus{Phase: v1alpha1.SessionPhase(phase)},
	}
}

func workspace(name, owner, template string) *v1alpha1.IDEWorkspace {
	return &v1alpha1.IDEWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		Spec:       v1alpha1.IDEWorkspaceSpec{Owner: owner, TemplateRef: template, Persistence: v1alpha1.WorkspacePersistence{Mode: v1alpha1.WorkspaceVolume}},
	}
}

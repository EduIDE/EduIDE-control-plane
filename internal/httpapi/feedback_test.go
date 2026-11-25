package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const (
	fbInstance = "abcde12345abcde12345"
	fbToken    = "instance-token-value"
	fbKey      = "installation-key"
)

type countingGets struct {
	client.Client
	n *int
}

func (c countingGets) Get(ctx context.Context, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	*c.n++
	return c.Client.Get(ctx, k, o, opts...)
}

type receiver struct {
	retryAfter string
	mu         sync.Mutex
	bodies     []string
	auth       []string
	status     int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies, r.auth = append(r.bodies, string(b)), append(r.auth, req.Header.Get("Authorization"))
	st := r.status
	r.mu.Unlock()
	if r.retryAfter != "" {
		w.Header().Set("Retry-After", r.retryAfter)
	}
	w.WriteHeader(st)
}

func feedbackEnv(t *testing.T, on bool) (*env, *receiver) {
	t.Helper()
	sess := &v1alpha1.IDESession{
		ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: testNS},
		Spec:       v1alpha1.IDESessionSpec{Owner: "alice", TemplateRef: "java"},
		Status:     v1alpha1.IDESessionStatus{Instance: fbInstance},
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ide-" + fbInstance + "-token", Namespace: testNS},
		Data:       map[string][]byte{v1alpha1.InstanceTokenKey: []byte(fbToken)},
	}
	idle := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ide-idleidleidleidleidle-token", Namespace: testNS},
		Data:       map[string][]byte{v1alpha1.InstanceTokenKey: []byte("idle-token")},
	}
	e := newEnv(t, sess, sec, idle)
	rcv := &receiver{status: 201}
	ts := httptest.NewServer(rcv)
	t.Cleanup(ts.Close)
	e.srv.cfg.Deployment, e.srv.cfg.Hosts.Service = "test", "ide.example.org"
	e.srv.cfg.Feedback = config.Feedback{Enabled: on, ReceiverURL: ts.URL + "/v1/feedback"}
	e.srv.EnableFeedback(fbKey)
	return e, rcv
}

func fbBody(title, desc string) map[string]any {
	return map[string]any{"kind": "bug", "title": title, "description": desc,
		"context": map[string]string{"version": "1.2.3", "appDefinition": "java", "browser": "Firefox 130"}}
}

func sendFeedback(e *env, body any, hdr ...string) *httptest.ResponseRecorder {
	h := append([]string{"X-EduIDE-Instance", fbInstance, "Authorization", "Bearer " + fbToken}, hdr...)
	return e.do("POST", "/feedback", "", body, h...)
}

func TestFeedbackOffIs404BeforeAnyCheck(t *testing.T) {
	e, rcv := feedbackEnv(t, false)
	// no token, no instance, invalid body: still 404
	if rec := e.do("POST", "/feedback", "", `not json`); rec.Code != 404 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := sendFeedback(e, fbBody("t", "d")); rec.Code != 404 || len(rcv.bodies) != 0 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestFeedbackAuth(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	for name, hdr := range map[string][]string{
		"no headers":      nil,
		"wrong token":     {"X-EduIDE-Instance", fbInstance, "Authorization", "Bearer nope"},
		"empty token":     {"X-EduIDE-Instance", fbInstance, "Authorization", "Bearer "},
		"unknown":         {"X-EduIDE-Instance", "zzzzzzzzzzzzzzzzzzzz", "Authorization", "Bearer " + fbToken},
		"bad instance id": {"X-EduIDE-Instance", "../x", "Authorization", "Bearer " + fbToken},
		"idle instance":   {"X-EduIDE-Instance", "idleidleidleidleidle", "Authorization", "Bearer idle-token"},
		"user jwt only":   {"Authorization", "Bearer alice"},
	} {
		if rec := e.do("POST", "/feedback", "", fbBody("t", "d"), hdr...); rec.Code != 401 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(rcv.bodies) != 0 {
		t.Fatal("nothing may be forwarded")
	}
}

func TestFeedbackValidation(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	good := fbBody("t", "d")
	for name, body := range map[string]any{
		"not json":     "nope",
		"empty title":  fbBody("", "d"),
		"long title":   fbBody(strings.Repeat("a", 121), "d"),
		"empty desc":   fbBody("t", ""),
		"long desc":    fbBody("t", strings.Repeat("a", 5001)),
		"bad kind":     map[string]any{"kind": "praise", "title": "t", "description": "d", "context": good["context"]},
		"extra field":  map[string]any{"kind": "bug", "title": "t", "description": "d", "context": good["context"], "user": "alice"},
		"unicode long": fbBody(strings.Repeat("ä", 121), "d"),
	} {
		if rec := sendFeedback(e, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := sendFeedback(e, fbBody(strings.Repeat("ä", 120), strings.Repeat("ö", 5000))); rec.Code != 202 {
		t.Errorf("limits are characters: %d %s", rec.Code, rec.Body)
	}
	if len(rcv.bodies) != 1 {
		t.Fatalf("invalid requests reached the receiver: %d", len(rcv.bodies))
	}
}

func TestFeedbackForwardsWithoutIdentity(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	rec := sendFeedback(e, fbBody("Crash", "see mail alice@example.org"))
	if rec.Code != 202 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rcv.auth[0] != "Bearer "+fbKey {
		t.Fatalf("auth %q", rcv.auth[0])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(rcv.bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got["kind"] != "bug" || got["installation"] != "test" || got["host"] != "ide.example.org" || got["title"] != "Crash" ||
		got["description"] != "see mail [email]" || got["context"].(map[string]any)["browser"] != "Firefox 130" {
		t.Fatalf("payload %v", got)
	}
	for _, leak := range []string{"alice", "Alice", fbInstance, fbToken, "s-1"} {
		if strings.Contains(rcv.bodies[0], leak) {
			t.Errorf("payload contains %q: %s", leak, rcv.bodies[0])
		}
	}
}

func TestFeedbackRateLimit(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	e.srv.now = func() time.Time { return now }
	for i := range 5 {
		if rec := sendFeedback(e, fbBody("t", "d")); rec.Code != 202 {
			t.Fatalf("%d: %d", i, rec.Code)
		}
		now = now.Add(time.Minute)
	}
	rec := sendFeedback(e, fbBody("t", "d"))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "3301" { // 1h minus 5 min, plus 1 s
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	now = now.Add(56 * time.Minute)
	if rec := sendFeedback(e, fbBody("t", "d")); rec.Code != 202 {
		t.Fatalf("slot after the window: %d", rec.Code)
	}
	if len(rcv.bodies) != 6 {
		t.Fatalf("%d forwarded", len(rcv.bodies))
	}
}

func TestFeedbackUpstreamFailure(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	rcv.status = 500
	for range 7 { // failed sends give the slot back
		if rec := sendFeedback(e, fbBody("t", "d")); rec.Code != 502 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	e.srv.cfg.Feedback.ReceiverURL = "http://127.0.0.1:1/x"
	if rec := sendFeedback(e, fbBody("t", "d")); rec.Code != 502 {
		t.Fatalf("unreachable: %d", rec.Code)
	}
}

func TestFeedbackIsNotOpenToBrowsers(t *testing.T) {
	e, _ := feedbackEnv(t, true)
	rec := sendFeedback(e, fbBody("t", "d"), "Origin", "https://evil.example.org")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header set")
	}
	req := httptest.NewRequest("OPTIONS", basePath+"/feedback", nil)
	req.Header.Set("Origin", "https://evil.example.org")
	req.Header.Set("Access-Control-Request-Method", "POST")
	pre := httptest.NewRecorder()
	e.handler.ServeHTTP(pre, req)
	if pre.Code < 400 || pre.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight answered: %d %v", pre.Code, pre.Header())
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"mail me at a.b+c@uni.example.org please":                 "mail me at [email] please",
		"matr 01234567 here":                                      "matr [matriculation number] here",
		"12345678 and 012345678 and 1012345678":                   "12345678 and 012345678 and 1012345678",
		"token ghp" + "_abcdefghijklmnopqrstuvwxyz0123456789 end": "token [token] end",
		"glpat" + "-abcdefghij1234567890 x":                       "[token] x",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln":               "[token]",
		"hex 0123456789abcdef0123456789abcdef0123 end":            "hex [token] end",
		"b64 QWxhZGRpbjpvcGVuIHNlc2FtZVF1aWNrQnJvd25Gb3g= x":      "b64 [token] x",
		"NullPointerException in Main.java:42":                    "NullPointerException in Main.java:42",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFeedbackLimitsAfterRedactionAndContext(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	// 120 x 8 digits become longer than the title limit after redaction
	title := strings.Repeat("01234567 ", 14)[:120]
	body := fbBody("  "+title+"  ", strings.Repeat("01234567 ", 560)[:5000])
	body["context"] = map[string]string{"version": "1.2.3<script>", "appDefinition": "evil", "browser": "Mozilla/5.0 (X11; Linux) " + strings.Repeat("abcdefghij ", 20)}
	if rec := sendFeedback(e, body); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Title, Description string
		Context            map[string]string
	}
	if err := json.Unmarshal([]byte(rcv.bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Title)); n != 120 {
		t.Errorf("title %d", n)
	}
	if n := len([]rune(got.Description)); n != 5000 {
		t.Errorf("description %d", n)
	}
	c := got.Context
	if c["version"] != "1.2.3script" || c["appDefinition"] != "java" || len(c["browser"]) != 100 || !strings.HasPrefix(c["browser"], "Mozilla/5.0 (X11 Linux) ") {
		t.Errorf("context %v", c)
	}
	if rec := sendFeedback(e, fbBody("   ", "d")); rec.Code != 400 {
		t.Errorf("blank title: %d", rec.Code)
	}
}

func TestFeedbackPassesReceiverRateLimit(t *testing.T) {
	e, rcv := feedbackEnv(t, true)
	rcv.status = 429
	rcv.retryAfter = "77"
	rec := sendFeedback(e, fbBody("t", "d"))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "77" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestFeedbackUnknownInstanceCostsNoSecretRead(t *testing.T) {
	e, _ := feedbackEnv(t, true)
	var reads int
	e.srv.w = countingGets{Client: e.srv.w, n: &reads}
	// token Secret exists for the idle instance, but no session owns it
	e.do("POST", "/feedback", "", fbBody("t", "d"), "X-EduIDE-Instance", "idleidleidleidleidle", "Authorization", "Bearer idle-token")
	if reads != 0 {
		t.Fatalf("%d secret reads", reads)
	}
}

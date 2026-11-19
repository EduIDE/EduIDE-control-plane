package httpapi

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// A launch must see the session of the previous launch even when the cache has not caught up.
func TestLaunchIdempotentWithStaleCache(t *testing.T) {
	e := newEnv(t)
	e.srv.r = fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(javaTemplate()).Build() // a cache that never sees sessions
	first := decode[Session](t, e.do("POST", "/sessions", "alice", launchBody(nil)))
	rec := e.do("POST", "/sessions", "alice", launchBody(nil))
	if rec.Code != 200 || decode[Session](t, rec).Name != first.Name {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Two launches with the same key in the same instant: neither sees the other in its list,
// the deterministic name makes the second one return the first.
func TestLaunchConcurrentSameKey(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(javaTemplate()).Build()
	k := interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
			if _, ok := l.(*v1alpha1.IDESessionList); ok {
				return nil
			}
			return c.List(ctx, l, opts...)
		},
	})
	srv := NewServer(testConfig(), k, k, NewHub(), nil)
	e := &env{t: t, srv: srv, handler: srv.Handler(verifier), k: k}
	first := e.do("POST", "/sessions", "alice", launchBody(nil))
	second := e.do("POST", "/sessions", "alice", launchBody(nil))
	if first.Code != 201 || second.Code != 200 || decode[Session](t, first).Name != decode[Session](t, second).Name {
		t.Fatalf("%d %s / %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	var l v1alpha1.IDESessionList
	_ = base.List(context.Background(), &l)
	if len(l.Items) != 1 {
		t.Fatalf("%d sessions", len(l.Items))
	}
}

// A Failed session holds the deterministic name; a relaunch still gets a new session.
func TestRelaunchAfterFailure(t *testing.T) {
	e := newEnv(t)
	first := decode[Session](t, e.do("POST", "/sessions", "alice", launchBody(nil)))
	var cr v1alpha1.IDESession
	_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: first.Name}, &cr)
	cr.Status.Phase = v1alpha1.SessionFailed
	if err := e.k.Update(context.Background(), &cr); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rec := e.do("POST", "/sessions", "alice", launchBody(nil))
		if rec.Code != 201 || decode[Session](t, rec).Name == first.Name {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: decode[Session](t, rec).Name}, &cr)
		cr.Status.Phase = v1alpha1.SessionFailed
		_ = e.k.Update(context.Background(), &cr)
	}
}

func TestLaunchSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	e := newEnv(t)
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	res := e.do("POST", "/sessions", "alice", launchBody(nil), "traceparent", "00-"+traceID+"-b7ad6b7169203331-01")
	if res.Code != 201 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	spans := rec.Ended()
	var names []string
	for _, c := range spans {
		names = append(names, c.Name())
	}
	if want := []string{"validate", "session.create", "secret.create", "launch"}; !slices.Equal(names, want) {
		t.Fatalf("spans %v, want %v", names, want)
	}
	sp := spans[3]
	for _, c := range spans[:3] {
		if c.Parent().SpanID() != sp.SpanContext().SpanID() {
			t.Errorf("%s is not a child of launch", c.Name())
		}
		for _, a := range c.Attributes() {
			for _, leak := range []string{"alice", "Alice", "secret-token-value", "git.example"} {
				if strings.Contains(a.Value.String(), leak) {
					t.Errorf("%s attribute %s leaks %q", c.Name(), a.Key, leak)
				}
			}
		}
	}
	if sp.Parent().TraceID().String() != traceID || sp.Parent().SpanID().String() != "b7ad6b7169203331" {
		t.Fatalf("parent %v", sp.Parent())
	}
	attrs := map[string]string{}
	for _, a := range sp.Attributes() {
		attrs[string(a.Key)] = a.Value.String()
		for _, leak := range []string{"alice", "Alice", "secret-token-value"} {
			if strings.Contains(a.Value.String(), leak) {
				t.Errorf("attribute %s leaks %q", a.Key, leak)
			}
		}
	}
	if attrs["eduide.template"] != "java" || attrs["eduide.user_id"] != telemetry.UserID([]byte("salt"), "alice") || attrs["eduide.session"] == "" {
		t.Fatalf("attributes %v", attrs)
	}
	// The session continues the trace below the launch span.
	var cr v1alpha1.IDESession
	_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: attrs["eduide.session"]}, &cr)
	if want := "00-" + traceID + "-" + sp.SpanContext().SpanID().String() + "-01"; cr.Annotations[v1alpha1.AnnotationTraceparent] != want {
		t.Fatalf("annotation %q, want %q", cr.Annotations[v1alpha1.AnnotationTraceparent], want)
	}

	// A rejected launch is marked as an error with the code only.
	e.do("POST", "/sessions", "alice", map[string]any{"template": "nope"})
	ended := rec.Ended()
	for _, n := range []int{len(ended) - 2, len(ended) - 1} { // validate and launch
		if sp := ended[n]; sp.Status().Description != string(CodeTemplateNotFound) {
			t.Fatalf("%s status %+v", sp.Name(), sp.Status())
		}
	}
}

func TestLaunchSpansWithWorkspace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	e := newEnv(t)
	if res := e.do("POST", "/sessions", "alice", launchBody(map[string]any{"persistence": "volume"})); res.Code != 201 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	var names []string
	for _, c := range rec.Ended() {
		names = append(names, c.Name())
		if c.Name() == "workspace.get_or_create" {
			attrs := map[string]string{}
			for _, a := range c.Attributes() {
				attrs[string(a.Key)] = a.Value.String()
			}
			if attrs["eduide.persistence"] != "volume" || attrs["eduide.template"] != "java" {
				t.Errorf("attributes %v", attrs)
			}
		}
	}
	if want := []string{"validate", "workspace.get_or_create", "session.create", "secret.create", "launch"}; !slices.Equal(names, want) {
		t.Fatalf("spans %v, want %v", names, want)
	}
}

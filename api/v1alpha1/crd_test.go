package v1alpha1_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// TestCRDValidation installs the generated CRDs into a real API server and
// checks that the CEL rules accept valid and reject invalid objects.
func TestCRDValidation(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	meta := func(n string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: n, Namespace: "default"} }

	tmpl := &v1.IDETemplate{ObjectMeta: meta("java"), Spec: v1.IDETemplateSpec{
		Image:       "example.org/ide:1",
		Persistence: v1.TemplatePersistence{Allowed: []v1.PersistenceMode{v1.PersistenceNone, v1.PersistenceVolume}, Default: v1.PersistenceNone},
		InjectableKeys: []v1.InjectableKey{
			{Name: "GIT_URI", Format: v1.FormatHTTPSURL},
			{Name: "GIT_USER", Source: v1.SourceUserName},
		},
		Timeouts: v1.Timeouts{Idle: &metav1.Duration{Duration: 3600e9}, Warning: &metav1.Duration{Duration: 3000e9}},
	}}
	mustCreate(t, c, ctx, tmpl)

	bad := tmpl.DeepCopy()
	bad.Name, bad.ResourceVersion = "bad-default", ""
	bad.Spec.Persistence = v1.TemplatePersistence{Allowed: []v1.PersistenceMode{v1.PersistenceNone}, Default: v1.PersistenceVolume}
	mustReject(t, c, ctx, bad, "default must be one of allowed")

	bad = tmpl.DeepCopy()
	bad.Name, bad.ResourceVersion = "bad-keys", ""
	bad.Spec.InjectableKeys = append(bad.Spec.InjectableKeys, v1.InjectableKey{Name: "GIT_URI", Format: v1.FormatHTTPSURL})
	mustReject(t, c, ctx, bad, "Duplicate value")

	bad = tmpl.DeepCopy()
	bad.Name, bad.ResourceVersion = "bad-format", ""
	bad.Spec.InjectableKeys = []v1.InjectableKey{{Name: "X"}}
	mustReject(t, c, ctx, bad, "format is required")

	bad = tmpl.DeepCopy()
	bad.Name, bad.ResourceVersion = "bad-timeouts", ""
	bad.Spec.Timeouts.Warning = &metav1.Duration{Duration: 7200e9}
	mustReject(t, c, ctx, bad, "warning must be shorter")

	bad = tmpl.DeepCopy()
	bad.Name, bad.ResourceVersion = "bad-max", ""
	bad.Spec.Timeouts.Max = &metav1.Duration{Duration: 60e9}
	mustReject(t, c, ctx, bad, "max must not be shorter than idle")

	// The API server must refuse what the operator cannot decode, such as "2d".
	rawTemplate := func(name string, spec map[string]any) *unstructured.Unstructured {
		base := map[string]any{"image": "example.org/ide:1", "persistence": map[string]any{"allowed": []any{"none"}, "default": "none"}}
		for k, v := range spec {
			base[k] = v
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": base}}
		u.SetGroupVersionKind(v1.GroupVersion.WithKind("IDETemplate"))
		u.SetName(name)
		u.SetNamespace("default")
		return u
	}
	for i, spec := range []map[string]any{
		{"timeouts": map[string]any{"idle": "2d"}},
		{"timeouts": map[string]any{"warning": "14"}},
		{"timeouts": map[string]any{"max": "soon"}},
		{"retention": "2d"},
	} {
		mustReject(t, c, ctx, rawTemplate("bad-duration-"+strconv.Itoa(i), spec), "should match")
	}
	mustCreate(t, c, ctx, rawTemplate("good-duration", map[string]any{"retention": "48h30m", "timeouts": map[string]any{"idle": "1.5h", "max": "36h"}}))

	rawWorkspace := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"owner": "alice", "templateRef": "java", "persistence": map[string]any{"mode": "volume"}, "retention": "2d"}}}
	rawWorkspace.SetGroupVersionKind(v1.GroupVersion.WithKind("IDEWorkspace"))
	rawWorkspace.SetName("bad-retention")
	rawWorkspace.SetNamespace("default")
	mustReject(t, c, ctx, rawWorkspace, "should match")

	mustCreate(t, c, ctx, &v1.IDEPool{ObjectMeta: meta("java"), Spec: v1.IDEPoolSpec{TemplateRef: "java", MinIdle: 1, TargetIdle: 2, MaxInstances: 10}})
	mustReject(t, c, ctx, &v1.IDEPool{ObjectMeta: meta("p2"), Spec: v1.IDEPoolSpec{TemplateRef: "java", MinIdle: 5, MaxInstances: 2}}, "minIdle must not exceed")

	s := &v1.IDESession{ObjectMeta: meta("s1"), Spec: v1.IDESessionSpec{Owner: "alice", TemplateRef: "java"}}
	mustCreate(t, c, ctx, s)
	s.Spec.Owner = "mallory"
	if err := c.Update(ctx, s); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("session spec change: want immutable error, got %v", err)
	}

	w := &v1.IDEWorkspace{ObjectMeta: meta("w1"), Spec: v1.IDEWorkspaceSpec{Owner: "alice", TemplateRef: "java", Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume}}}
	mustCreate(t, c, ctx, w)
	w.Spec.Owner = "mallory"
	if err := c.Update(ctx, w); err == nil || !strings.Contains(err.Error(), "owner is immutable") {
		t.Fatalf("workspace owner change: want immutable error, got %v", err)
	}
}

func mustCreate(t *testing.T, c client.Client, ctx context.Context, o client.Object) {
	t.Helper()
	if err := c.Create(ctx, o); err != nil {
		t.Fatalf("create %s: %v", o.GetName(), err)
	}
}

func mustReject(t *testing.T, c client.Client, ctx context.Context, o client.Object, msg string) {
	t.Helper()
	err := c.Create(ctx, o)
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("create %s: want error containing %q, got %v", o.GetName(), msg, err)
	}
}

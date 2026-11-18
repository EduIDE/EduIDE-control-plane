package authz

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// TestCacheSessionsEnvtest checks the field index and the managed-by filter against a real API server.
func TestCacheSessionsEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mk := func(name, owner, instance string, managed bool) {
		s := &v1alpha1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: v1alpha1.IDESessionSpec{Owner: owner, TemplateRef: "t"}}
		if managed {
			s.Labels = map[string]string{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue}
		}
		if err := c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		s.Status = v1alpha1.IDESessionStatus{Phase: v1alpha1.SessionReady, Instance: instance}
		if err := c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	mk("s-a", "alice", instanceA, true)
	mk("s-unmanaged", "mallory", "dddddddddddddddddddd", false)

	ca, err := cache.New(cfg, cache.Options{Scheme: scheme, DefaultNamespaces: map[string]cache.Config{"default": {}},
		ByObject: map[client.Object]cache.ByObject{&v1alpha1.IDESession{}: {Label: labels.SelectorFromSet(labels.Set{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue})}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := IndexSessions(ctx, ca); err != nil {
		t.Fatal(err)
	}
	go func() { _ = ca.Start(ctx) }()
	if !ca.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	s := CacheSessions{Cache: ca, Namespace: "default"}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := s.ByInstance(ctx, instanceA)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 1 && got[0].Spec.Owner == "alice" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session not found: %v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got, _ := s.ByInstance(ctx, "dddddddddddddddddddd"); len(got) != 0 {
		t.Fatalf("unmanaged session visible: %v", got)
	}
	if got, _ := s.ByInstance(ctx, ""); len(got) != 0 {
		t.Fatalf("empty instance matched: %v", got)
	}
}

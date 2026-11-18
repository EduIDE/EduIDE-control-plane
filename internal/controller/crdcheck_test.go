package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func fakeDiscovery(names ...string) *fakediscovery.FakeDiscovery {
	l := &metav1.APIResourceList{GroupVersion: v1.GroupVersion.String()}
	for _, n := range names {
		l.APIResources = append(l.APIResources, metav1.APIResource{Name: n})
	}
	return &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{l}}}
}

func TestCheckCRDs(t *testing.T) {
	if err := CheckCRDs(fakeDiscovery(requiredResources...)); err != nil {
		t.Errorf("complete: %v", err)
	}
	err := CheckCRDs(fakeDiscovery("idetemplates", "idetemplates/status"))
	if err == nil || !strings.Contains(err.Error(), "idesessions") || !strings.Contains(err.Error(), "eduide-control-plane-crds") {
		t.Errorf("missing resources: %v", err)
	}
	empty := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	if err := CheckCRDs(empty); err == nil {
		t.Error("no group version: want an error")
	}
}

func TestCheckCRDsAgainstEnvtest(t *testing.T) {
	requireEnv(t)
	if err := CheckCRDs(discovery.NewDiscoveryClientForConfigOrDie(restCfg)); err != nil {
		t.Fatal(err)
	}
}

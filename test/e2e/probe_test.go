//go:build e2e

package e2e

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// runProbe runs a shell script in a pod without any eduide label and returns its output.
// The fake IDE image has busybox (sh, nc).
func runProbe(t *testing.T, script string) string {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-probe-", Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "probe",
				Image:           "ghcr.io/eduide/eduide-control-plane/fake-ide:e2e",
				ImagePullPolicy: corev1.PullNever,
				Command:         []string{"sh", "-c", script},
			}},
		},
	}
	if err := kube.Create(ctx(), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kube.Delete(ctx(), p) })
	eventually(t, 2*time.Minute, "probe pod finished", func() bool {
		if err := kube.Get(ctx(), client.ObjectKeyFromObject(p), p); err != nil {
			return false
		}
		return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
	})
	out, err := logsOf(ctx(), p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

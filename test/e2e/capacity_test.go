//go:build e2e

package e2e

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// G8, U7: a session that cannot be placed in node pool a makes the operator delete the oldest idle pod
// of that node pool. Node pool b stays untouched. After exhaustedAfter the session fails with capacity-exhausted.
// The template cap-a-big requests more CPU than any node has, so its pod is Unschedulable.
func TestCapacityPressurePerNodePool(t *testing.T) {
	waitPoolIdle(t, "cap-a", 2)
	waitPoolIdle(t, "cap-b", 2)
	idleA, idleB := idlePods(t, "cap-a"), idlePods(t, "cap-b")
	oldestA := idleA[0].UID
	for _, p := range idleA {
		if p.Labels[v1alpha1.LabelNodePool] != "pool-a" {
			t.Fatalf("pod %s has node-pool label %q, want pool-a", p.Name, p.Labels[v1alpha1.LabelNodePool])
		}
	}
	t.Cleanup(func() {
		// The pools refill after the pressure is gone; the next tests expect idle pods.
		waitPoolIdle(t, "cap-a", 2)
	})

	a := newAPI(t, alice)
	s := a.mustLaunch(LaunchRequest{Template: "cap-a-big"})
	start := time.Now()

	oldestGone := false
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var p corev1.Pod
		for _, q := range sessionPods(t, nil) {
			if q.UID == oldestA {
				p = q
			}
		}
		oldestGone = oldestGone || p.UID == "" || p.DeletionTimestamp != nil
		if _, cur := a.get(s.Name); cur.Phase == "Failed" {
			s = cur
			break
		}
		time.Sleep(time.Second)
	}
	t.Logf("session failed after %s", time.Since(start).Round(time.Second))

	if s.Phase != "Failed" || s.ErrorCode != "capacity-exhausted" {
		t.Fatalf("phase %q code %q (%s), want Failed capacity-exhausted", s.Phase, s.ErrorCode, s.Message)
	}
	if !oldestGone {
		t.Error("the oldest idle pod of node pool a was not deleted")
	}
	for _, p := range idleB {
		var cur corev1.Pod
		found := false
		for _, q := range sessionPods(t, map[string]string{v1alpha1.LabelTemplate: "cap-b"}) {
			if q.UID == p.UID {
				cur, found = q, true
			}
		}
		if !found || cur.DeletionTimestamp != nil {
			t.Errorf("idle pod %s of node pool b was touched", p.Name)
		}
	}
}

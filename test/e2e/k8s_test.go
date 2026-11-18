//go:build e2e

package e2e

import (
	"context"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func ctx() context.Context { return context.Background() }

// sessionPods lists session pods with the given labels, oldest first.
func sessionPods(t testing.TB, labels map[string]string) []corev1.Pod {
	t.Helper()
	sel := client.MatchingLabels{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue, v1alpha1.LabelComponent: v1alpha1.ComponentSession}
	for k, v := range labels {
		sel[k] = v
	}
	var l corev1.PodList
	if err := kube.List(ctx(), &l, client.InNamespace(namespace), sel); err != nil {
		t.Fatal(err)
	}
	sort.Slice(l.Items, func(i, j int) bool { return l.Items[i].CreationTimestamp.Before(&l.Items[j].CreationTimestamp) })
	return l.Items
}

func idlePods(t testing.TB, template string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range sessionPods(t, map[string]string{v1alpha1.LabelTemplate: template, v1alpha1.LabelState: v1alpha1.StateIdle}) {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out
}

// waitPoolIdle waits until the pool of the template has at least n idle ready instances.
func waitPoolIdle(t testing.TB, template string, n int32) {
	t.Helper()
	eventually(t, 3*time.Minute, "pool "+template+" idleReady", func() bool {
		var p v1alpha1.IDEPool
		if err := kube.Get(ctx(), client.ObjectKey{Namespace: namespace, Name: template}, &p); err != nil {
			return false
		}
		return p.Status.IdleReady >= n
	})
}

func podOfSession(t testing.TB, s Session) corev1.Pod {
	t.Helper()
	pods := sessionPods(t, map[string]string{v1alpha1.LabelSession: s.Name})
	if len(pods) != 1 {
		t.Fatalf("session %s: %d pods", s.Name, len(pods))
	}
	return pods[0]
}

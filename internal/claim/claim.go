// Package claim holds the pure logic of pools and claims: which idle pod a
// session takes, how many idle pods a pool keeps and which ones go first.
package claim

import (
	"crypto/rand"
	"encoding/base32"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// Ready reports whether the pod is running and ready for traffic.
func Ready(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Usable reports whether an idle pod can still serve: not deleting and not terminated.
func Usable(p *corev1.Pod) bool {
	return p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodFailed && p.Status.Phase != corev1.PodSucceeded
}

// stuckWaiting are container waiting reasons that do not heal by waiting: a wrong image name or tag, no pull access, a broken container config.
var stuckWaiting = map[string]bool{"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true, "CreateContainerConfigError": true, "CrashLoopBackOff": true}

// StuckWaiting returns the container and the waiting reason of a container that waits for a reason that does not heal, or "".
func StuckWaiting(p *corev1.Pod) (container, reason string) {
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if w := cs.State.Waiting; w != nil && stuckWaiting[w.Reason] {
				return cs.Name, w.Reason
			}
		}
	}
	return "", ""
}

// Broken returns why an idle pod must not be handed out: a container waits for a reason that does not heal, or has restarted. "" for a sound pod.
func Broken(p *corev1.Pod) string {
	if _, reason := StuckWaiting(p); reason != "" {
		return reason
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if cs.RestartCount > 0 {
				return "restarted"
			}
		}
	}
	return ""
}

// Unschedulable reports whether the scheduler could not place the pod.
func Unschedulable(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return true
		}
	}
	return false
}

func older(a, b *corev1.Pod) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// Idle returns the usable pods in state idle.
func Idle(pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for i := range pods {
		if pods[i].Labels[v1.LabelState] == v1.StateIdle && Usable(&pods[i]) {
			out = append(out, pods[i])
		}
	}
	return out
}

// Candidates orders idle pods for a claim: ready ones first, oldest first, then
// the starting ones when includeStarting is set, oldest first.
func Candidates(pods []corev1.Pod, includeStarting bool) []corev1.Pod {
	var ready, starting []corev1.Pod
	for _, p := range Idle(pods) {
		if Broken(&p) != "" {
			continue
		}
		if Ready(&p) {
			ready = append(ready, p)
		} else if includeStarting {
			starting = append(starting, p)
		}
	}
	byAge := func(s []corev1.Pod) {
		sort.Slice(s, func(i, j int) bool { return older(&s[i], &s[j]) })
	}
	byAge(ready)
	byAge(starting)
	return append(ready, starting...)
}

// EffectiveIdle is clamp(max(minIdle, targetIdle), 0, maxInstances-claimed), and 0 under capacity pressure.
func EffectiveIdle(minIdle, targetIdle, maxInstances, claimed int32, pressure bool) int32 {
	if pressure {
		return 0
	}
	return max(0, min(max(minIdle, targetIdle), maxInstances-claimed))
}

// Surplus picks n idle pods to delete: starting pods before ready ones, newest first.
func Surplus(idle []corev1.Pod, n int) []corev1.Pod {
	s := append([]corev1.Pod(nil), idle...)
	sort.Slice(s, func(i, j int) bool {
		ri, rj := Ready(&s[i]), Ready(&s[j])
		if ri != rj {
			return !ri
		}
		return older(&s[j], &s[i])
	})
	if n < 0 {
		n = 0
	}
	return s[:min(n, len(s))]
}

// OldestIdle is the idle pod of a node pool (label value, "" for none) to evict, or nil:
// the oldest one on a node, because only that frees node capacity, else the oldest pending one (quota).
func OldestIdle(pods []corev1.Pod, nodePool string) *corev1.Pod {
	var best *corev1.Pod
	idle := Idle(pods)
	better := func(a, b *corev1.Pod) bool {
		if (a.Spec.NodeName != "") != (b.Spec.NodeName != "") {
			return a.Spec.NodeName != ""
		}
		return older(a, b)
	}
	for i := range idle {
		if idle[i].Labels[v1.LabelNodePool] == nodePool && (best == nil || better(&idle[i], best)) {
			best = &idle[i]
		}
	}
	return best
}

// NewInstanceID returns 20 lowercase base32 characters from 12 random bytes.
func NewInstanceID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

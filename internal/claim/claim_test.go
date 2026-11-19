package claim

import (
	"regexp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func pod(name string, ageMin int, ready bool, state string) corev1.Pod {
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:              name,
		CreationTimestamp: metav1.NewTime(epoch.Add(time.Duration(ageMin) * time.Minute)),
		Labels:            map[string]string{v1.LabelState: state},
	}}
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}
	return p
}

func names(ps []corev1.Pod) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCandidates(t *testing.T) {
	deleting := pod("deleting", 0, true, v1.StateIdle)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	failed := pod("failed", 0, false, v1.StateIdle)
	failed.Status.Phase = corev1.PodFailed
	pods := []corev1.Pod{
		pod("s-new", 5, false, v1.StateIdle),
		pod("r-new", 4, true, v1.StateIdle),
		pod("s-old", 1, false, v1.StateIdle),
		pod("r-old", 2, true, v1.StateIdle),
		pod("claimed", 0, true, v1.StateClaimed),
		deleting, failed,
	}
	if got := names(Candidates(pods, true)); !eq(got, []string{"r-old", "r-new", "s-old", "s-new"}) {
		t.Errorf("with starting: %v", got)
	}
	if got := names(Candidates(pods, false)); !eq(got, []string{"r-old", "r-new"}) {
		t.Errorf("without starting: %v", got)
	}
	if got := Candidates(nil, true); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestEffectiveIdle(t *testing.T) {
	tests := []struct {
		name                      string
		min, target, max, claimed int32
		pressure                  bool
		want                      int32
	}{
		{"target wins", 1, 5, 10, 0, false, 5},
		{"floor wins", 3, 1, 10, 0, false, 3},
		{"capped by room", 1, 8, 10, 6, false, 4},
		{"over capacity", 1, 8, 10, 12, false, 0},
		{"pressure", 3, 5, 10, 0, true, 0},
		{"zero", 0, 0, 10, 0, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveIdle(tc.min, tc.target, tc.max, tc.claimed, tc.pressure); got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestSurplus(t *testing.T) {
	idle := []corev1.Pod{
		pod("r-old", 1, true, v1.StateIdle),
		pod("r-new", 4, true, v1.StateIdle),
		pod("s-old", 2, false, v1.StateIdle),
		pod("s-new", 5, false, v1.StateIdle),
	}
	if got := names(Surplus(idle, 3)); !eq(got, []string{"s-new", "s-old", "r-new"}) {
		t.Errorf("got %v", got)
	}
	if got := Surplus(idle, 10); len(got) != 4 {
		t.Errorf("clamp: %d", len(got))
	}
	if got := Surplus(idle, -1); len(got) != 0 {
		t.Errorf("negative: %d", len(got))
	}
}

func TestOldestIdle(t *testing.T) {
	a, b, c := pod("a", 3, true, v1.StateIdle), pod("b", 1, true, v1.StateIdle), pod("c", 0, true, v1.StateClaimed)
	a.Labels[v1.LabelNodePool], b.Labels[v1.LabelNodePool], c.Labels[v1.LabelNodePool] = "x", "y", "x"
	pods := []corev1.Pod{a, b, c}
	if p := OldestIdle(pods, "x"); p == nil || p.Name != "a" {
		t.Errorf("x: %v", p)
	}
	if p := OldestIdle(pods, "z"); p != nil {
		t.Errorf("z: %v", p)
	}
	// A pod on a node goes before an older pending one: only it frees node capacity.
	pending, placed := pod("pending", 0, false, v1.StateIdle), pod("placed", 5, true, v1.StateIdle)
	pending.Labels[v1.LabelNodePool], placed.Labels[v1.LabelNodePool] = "x", "x"
	placed.Spec.NodeName = "node-1"
	if p := OldestIdle([]corev1.Pod{pending, placed}, "x"); p == nil || p.Name != "placed" {
		t.Errorf("placed first: %v", p)
	}
	if p := OldestIdle([]corev1.Pod{pending}, "x"); p == nil || p.Name != "pending" {
		t.Errorf("pending only: %v", p)
	}
}

func TestNewInstanceID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{20}$`)
	seen := map[string]bool{}
	for range 100 {
		id := NewInstanceID()
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestStuckWaiting(t *testing.T) {
	waiting := func(reason string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "ide", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
	}
	tests := []struct {
		name         string
		p            corev1.Pod
		wantContName string
		want         string
	}{
		{"pulling is fine", corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("ContainerCreating")}}}, "", ""},
		{"image pull backoff", corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("ImagePullBackOff")}}}, "ide", "ImagePullBackOff"},
		{"init container", corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{waiting("ErrImagePull")}}}, "ide", "ErrImagePull"},
		{"config error", corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("CreateContainerConfigError")}}}, "ide", "CreateContainerConfigError"},
		{"no status", corev1.Pod{}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if c, r := StuckWaiting(&tc.p); c != tc.wantContName || r != tc.want {
				t.Errorf("got %q %q", c, r)
			}
		})
	}
}

func TestBrokenPodsAreNeverCandidates(t *testing.T) {
	ok := pod("ok", 1, true, v1.StateIdle)
	crash := pod("crash", 0, false, v1.StateIdle)
	crash.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "ide", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	restarted := pod("restarted", 0, true, v1.StateIdle)
	restarted.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", RestartCount: 1}}
	got := Candidates([]corev1.Pod{crash, restarted, ok}, true)
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("candidates: %v", got)
	}
	if Broken(&crash) != "CrashLoopBackOff" || Broken(&restarted) != "restarted" || Broken(&ok) != "" {
		t.Error("Broken reasons")
	}
}

func TestHopelessSparesPullsThatCanHeal(t *testing.T) {
	w := func(reason, msg string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "ide",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}}}}}
	}
	for _, tc := range []struct {
		p    *corev1.Pod
		want string
	}{
		{w("ErrImagePull", "rpc error: code = NotFound desc = failed to pull and unpack image: not found"), "ErrImagePull"},
		{w("ImagePullBackOff", `Back-off pulling image "x": ErrImagePull: pull access denied`), "ImagePullBackOff"},
		{w("ErrImagePull", "401 Unauthorized"), "ErrImagePull"},
		{w("ErrImagePull", "dial tcp 1.2.3.4:443: i/o timeout"), ""},
		{w("ErrImagePull", "429 Too Many Requests"), ""},
		{w("ImagePullBackOff", `Back-off pulling image "x"`), ""},
		{w("InvalidImageName", ""), "InvalidImageName"},
		{w("CreateContainerConfigError", "secret not found"), "CreateContainerConfigError"},
		{w("ContainerCreating", ""), ""},
	} {
		if _, got := Hopeless(tc.p); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.p.Status.ContainerStatuses[0].State.Waiting, got, tc.want)
		}
	}
}

func TestStarted(t *testing.T) {
	st := func(cs ...corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: cs}}
	}
	creating := corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}
	running := corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	backoff := corev1.ContainerStatus{RestartCount: 2, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	if Started(st()) || Started(st(creating)) || !Started(st(creating, running)) || !Started(st(backoff)) {
		t.Error("Started")
	}
}

func TestOutOfCapacity(t *testing.T) {
	sched := func(status corev1.ConditionStatus, reason, msg string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: status, Reason: reason, Message: msg}}}}
	}
	for name, tc := range map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"no condition":    {&corev1.Pod{}, false},
		"scheduled":       {sched(corev1.ConditionTrue, "", ""), false},
		"no message":      {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, ""), true},
		"cpu":             {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, "0/3 nodes are available: 3 Insufficient cpu."), true},
		"pods":            {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, "0/3 nodes are available: 3 Too many pods."), true},
		"mixed":           {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, "0/3 nodes are available: 1 node(s) had volume node affinity conflict, 2 Insufficient memory."), true},
		"volume affinity": {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, "0/3 nodes are available: 3 node(s) had volume node affinity conflict."), false},
		"selector":        {sched(corev1.ConditionFalse, corev1.PodReasonUnschedulable, "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector."), false},
		"other reason":    {sched(corev1.ConditionFalse, "SchedulerError", "Insufficient cpu"), false},
	} {
		if got := OutOfCapacity(tc.pod); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

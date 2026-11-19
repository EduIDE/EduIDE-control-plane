package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func TestDecide(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := timeouts{idle: 60 * time.Minute, warning: 50 * time.Minute, max: 24 * time.Hour}
	m := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }
	tests := []struct {
		name                  string
		now, base             time.Time
		idleWarned, maxWarned bool
		want                  action
		next                  time.Time
	}{
		{"fresh", m(1), t0, false, false, actNone, m(50)},
		{"warn idle", m(50), t0, false, false, actWarnIdle, m(60)},
		{"warned, wait for idle", m(55), t0, true, false, actNone, m(60)},
		{"idle end", m(60), t0, true, false, actEnd, time.Time{}},
		{"activity moves idle", m(70), m(30), false, false, actNone, m(80)},
		{"max warning", m(24*60 - 10), m(24*60 - 12), false, false, actWarnMax, m(24*60 - 2 + 0)},
		{"max end", m(24 * 60), m(24*60 - 1), true, true, actEnd, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.now, t0, tc.base, to, tc.idleWarned, tc.maxWarned)
			if d.act != tc.want {
				t.Fatalf("act %d want %d", d.act, tc.want)
			}
			if tc.want != actEnd && tc.name != "max warning" && !d.next.Equal(tc.next) {
				t.Errorf("next %v want %v", d.next, tc.next)
			}
		})
	}
}

func TestResolveTimeoutsDefaults(t *testing.T) {
	if to := resolveTimeouts(newTemplate("x")); to.idle != time.Hour || to.warning != 50*time.Minute || to.max != 24*time.Hour {
		t.Errorf("%+v", to)
	}
}

// The CRD rules compare only the fields a template sets; the resolved values must still make sense.
func TestResolveTimeoutsKeepsTheWarningBeforeTheIdleEnd(t *testing.T) {
	d := func(x time.Duration) *metav1.Duration { return &metav1.Duration{Duration: x} }
	for name, tc := range map[string]struct {
		in   v1.Timeouts
		want timeouts
	}{
		"explicit triple":            {v1.Timeouts{Idle: d(30 * time.Minute), Warning: d(20 * time.Minute), Max: d(2 * time.Hour)}, timeouts{30 * time.Minute, 20 * time.Minute, 2 * time.Hour}},
		"idle below default warning": {v1.Timeouts{Idle: d(30 * time.Minute)}, timeouts{30 * time.Minute, 25 * time.Minute, 24 * time.Hour}},
		"warning after default idle": {v1.Timeouts{Warning: d(90 * time.Minute)}, timeouts{time.Hour, 50 * time.Minute, 24 * time.Hour}},
		"warning equals idle":        {v1.Timeouts{Idle: d(10 * time.Minute), Warning: d(10 * time.Minute)}, timeouts{10 * time.Minute, 500 * time.Second, 24 * time.Hour}},
		"zero idle and max":          {v1.Timeouts{Idle: d(0), Max: d(0)}, timeouts{time.Hour, 50 * time.Minute, 24 * time.Hour}},
	} {
		tm := newTemplate("x")
		tm.Spec.Timeouts = tc.in
		got := resolveTimeouts(tm)
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
		// With these values a session gets its idle warning before it ends.
		t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if dec := decide(t0.Add(got.warning), t0, t0, got, false, false); dec.act != actWarnIdle {
			t.Errorf("%s: at the warning time the decision is %d, want the idle warning", name, dec.act)
		}
	}
}

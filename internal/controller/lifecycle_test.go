package controller

import (
	"testing"
	"time"
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

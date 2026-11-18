package controller

import (
	"time"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

type timeouts struct{ idle, warning, max time.Duration }

func resolveTimeouts(t *v1.IDETemplate) timeouts {
	to := timeouts{idle: time.Hour, warning: 50 * time.Minute, max: 24 * time.Hour}
	if d := t.Spec.Timeouts.Idle; d != nil {
		to.idle = d.Duration
	}
	if d := t.Spec.Timeouts.Warning; d != nil {
		to.warning = d.Duration
	}
	if d := t.Spec.Timeouts.Max; d != nil {
		to.max = d.Duration
	}
	return to
}

type action int

const (
	actNone action = iota
	actWarnIdle
	actWarnMax
	actEnd
)

// decision is what to do with a running session and when to look again.
type decision struct {
	act action
	// at is when the session ends for the notice that act asks for.
	at   time.Time
	next time.Time
}

// decide applies idle and maximum lifetime. base is the latest activity (claim time at least).
// A warning goes out once: the caller passes whether it was sent already.
func decide(now, created, base time.Time, to timeouts, idleWarned, maxWarned bool) decision {
	idleAt, maxAt := base.Add(to.idle), created.Add(to.max)
	if !now.Before(idleAt) || !now.Before(maxAt) {
		return decision{act: actEnd}
	}
	lead := to.idle - to.warning
	warnIdle, warnMax := base.Add(to.warning), maxAt.Add(-lead)
	d := decision{next: idleAt}
	if maxAt.Before(d.next) {
		d.next = maxAt
	}
	consider := func(t time.Time) {
		if t.After(now) && t.Before(d.next) {
			d.next = t
		}
	}
	if !idleWarned {
		consider(warnIdle)
	}
	if !maxWarned {
		consider(warnMax)
	}
	switch {
	case !idleWarned && !now.Before(warnIdle):
		d.act, d.at = actWarnIdle, idleAt
	case !maxWarned && !now.Before(warnMax):
		d.act, d.at = actWarnMax, maxAt
	}
	return d
}

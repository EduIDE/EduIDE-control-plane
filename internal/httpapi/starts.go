package httpapi

import (
	"slices"
	"sync"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	startsCap         = 512 // samples kept per template
	startsShortWindow = time.Hour
	startsLongWindow  = 24 * time.Hour
	startsLongMin     = 3 // the long window needs this many samples
)

type startSample struct {
	at     time.Time // when the session became Ready
	dur    time.Duration
	warm   bool // claimed an idle ready instance
	volume bool // runs on a workspace, never claims from a pool
}

// startStats keeps the observed start durations per template (U11).
// shortcut: in memory per replica, a restart refills it from the sessions the informer replays; persist when estimates must survive empty clusters.
type startStats struct {
	mu   sync.Mutex
	byTp map[string][]startSample
	// outcomes are all starts of all templates, Ready or Failed, for the public status (U17).
	outcomes []startOutcome
}

type startOutcome struct {
	at     time.Time
	dur    time.Duration
	failed bool
}

const outcomesCap = 2048

func (st *startStats) addOutcome(o startOutcome) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.outcomes = append(st.outcomes, o)
	if len(st.outcomes) > outcomesCap {
		st.outcomes = st.outcomes[len(st.outcomes)-outcomesCap:]
	}
}

// recordFailure stores a session that turned Failed, at the time of its Ready=False condition (the replay
// after a restart then does not count old failures as new). It reports whether it stored one.
func (st *startStats) recordFailure(s *v1alpha1.IDESession, now time.Time) bool {
	if s.Status.Phase != v1alpha1.SessionFailed {
		return false
	}
	at := now
	for _, c := range s.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && !c.LastTransitionTime.IsZero() {
			at = c.LastTransitionTime.Time
		}
	}
	st.addOutcome(startOutcome{at: at, failed: true})
	return true
}

func newStartStats() *startStats { return &startStats{byTp: map[string][]startSample{}} }

// record stores the start of a session that is Ready. It reports whether it stored a sample.
func (st *startStats) record(s *v1alpha1.IDESession) bool {
	ready := s.Status.Timestamps.Ready
	if s.Status.Phase != v1alpha1.SessionReady || ready == nil || s.Status.StartStrategy == "" {
		return false
	}
	d := ready.Sub(s.CreationTimestamp.Time)
	if d < 0 {
		return false
	}
	st.addOutcome(startOutcome{at: ready.Time, dur: d})
	st.add(s.Spec.TemplateRef, startSample{at: ready.Time, dur: d, warm: s.Status.StartStrategy == v1alpha1.StartWarmReady, volume: s.Spec.WorkspaceRef != ""})
	return true
}

func (st *startStats) add(template string, x startSample) {
	st.mu.Lock()
	defer st.mu.Unlock()
	l := append(st.byTp[template], x)
	if len(l) > startsCap {
		l = l[len(l)-startsCap:]
	}
	st.byTp[template] = l
}

// estimate is the median start duration of the matching samples: of the last hour, else of the last 24 h with at least 3 samples.
func (st *startStats) estimate(template string, now time.Time, match func(startSample) bool) *StartEstimate {
	st.mu.Lock()
	var short, long []time.Duration
	for _, x := range st.byTp[template] {
		age := now.Sub(x.at)
		if !match(x) || age > startsLongWindow {
			continue
		}
		long = append(long, x.dur)
		if age <= startsShortWindow {
			short = append(short, x.dur)
		}
	}
	st.mu.Unlock()
	use := short
	if len(use) == 0 {
		if len(long) < startsLongMin {
			return nil
		}
		use = long
	}
	slices.Sort(use)
	m := use[len(use)/2]
	if len(use)%2 == 0 {
		m = (use[len(use)/2-1] + use[len(use)/2]) / 2
	}
	return &StartEstimate{Seconds: m.Seconds(), Samples: len(use)}
}

// startEstimates builds Template.startEstimate. Without persistence a warm median counts while the pool has an
// idle ready instance; with a workspace a start is always cold.
func (st *startStats) startEstimates(t *v1alpha1.IDETemplate, idleReady bool, now time.Time) *StartEstimates {
	var out StartEstimates
	for _, m := range t.Spec.Persistence.Allowed {
		switch m {
		case v1alpha1.PersistenceNone:
			if e := st.estimate(t.Name, now, func(x startSample) bool { return !x.volume && x.warm == idleReady }); e != nil {
				e.Strategy = StartEstimateStrategyCold
				if idleReady {
					e.Strategy = StartEstimateStrategyWarm
				}
				out.None = e
			}
		case v1alpha1.PersistenceVolume:
			if e := st.estimate(t.Name, now, func(x startSample) bool { return x.volume }); e != nil {
				e.Strategy = StartEstimateStrategyCold
				out.Volume = e
			}
		}
	}
	if out.None == nil && out.Volume == nil {
		return nil
	}
	return &out
}

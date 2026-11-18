// Package model holds the types every other package shares: the snapshot the
// providers fill, the events they report and the recommendations strategies
// return.
package model

import "time"

// Snapshot is everything a strategy may look at for one decision.
// Strategies see only this, never the Kubernetes API.
type Snapshot struct {
	Now    time.Time
	Apps   map[string]*App
	Events []Event
}

// App is the state of one IDEPool.
type App struct {
	Name         string
	MinIdle      int
	MaxInstances int
	// TargetIdle is the live spec.targetIdle, nil when unset.
	TargetIdle *int

	// Template is spec.templateRef, the key of the Prometheus metrics.
	Template string
	// CapacityPressure is the pool's CapacityPressure condition: the operator
	// holds 0 idle instances, whatever targetIdle says.
	CapacityPressure bool

	IdleReady    int
	IdleStarting int
	Claimed      int

	// ClaimRate is claims per second over the reactive window, from Prometheus.
	// Negative means unknown.
	ClaimRate float64
	// ReadySeconds is the p95 time from pool deployment created to pod Ready.
	// Zero means unknown.
	ReadySeconds float64

	// PodCPU (cores) and PodMemory (bytes) are the requests of one pool pod.
	PodCPU    float64
	PodMemory float64

	Annotations map[string]string
}

// Idle counts ready and starting pods, which is what targetIdle refers to.
func (a *App) Idle() int { return a.IdleReady + a.IdleStarting }

// EventKind says how an event drives demand.
type EventKind string

const (
	// EventExam is an exam: nearly everyone registered starts at once.
	EventExam EventKind = "exam"
	// EventRelease is an exercise becoming available.
	EventRelease EventKind = "release"
	// EventDue is an exercise deadline, with the rush before it.
	EventDue EventKind = "due"
)

// Event is one date that drives demand for a pool.
type Event struct {
	Pool string
	Kind EventKind
	Time time.Time
	// End is set for exams, zero otherwise.
	End          time.Time
	Participants int
	Source       string
}

// Recommendation is one strategy's wish for one Pool.
type Recommendation struct {
	Pool       string
	TargetIdle int
	Reason     string
	// Forecast is the expected number of starts within the horizon, if the
	// strategy forecasts at all.
	Forecast float64
	// Distribution is P(demand <= level), ascending by level, if the strategy
	// has one.
	Distribution []LevelProbability
}

// LevelProbability is one point of a cumulative demand distribution.
type LevelProbability struct {
	Level       int
	Probability float64
}

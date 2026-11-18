package telemetry

import "github.com/prometheus/client_golang/prometheus"

// Metrics are a contract (docs/contracts/metrics.md); the pool scaler and the
// dashboards depend on names and labels. Change them only additively.
var (
	SessionStartup = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_session_startup_seconds",
		Help:    "Time from IDESession creation until phase Ready or Failed.",
		Buckets: []float64{0.25, 0.5, 1, 2, 3, 5, 10, 20, 30, 45, 60, 120},
	}, []string{"template", "strategy", "result", "persistence"})
	SessionFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_session_failures_total",
		Help: "Sessions that ended in phase Failed by template and stable error code.",
	}, []string{"template", "code"})
	SessionPhase = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_session_phase_seconds",
		Help:    "Duration of one phase of the start path.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"phase", "strategy"})
	ClaimTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_claim_total",
		Help: "Claim attempts by result: claimed, conflict, empty.",
	}, []string{"template", "result"})
	PoolInstances = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_pool_instances",
		Help: "Instances of a pool by state: idle_ready, idle_starting, claimed.",
	}, []string{"pool", "state"})
	PoolTargetIdle = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_pool_target_idle",
		Help: "Effective idle target of a pool after floor, cap and capacity pressure.",
	}, []string{"pool"})
	CapacityPressure = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_capacity_pressure",
		Help: "1 while a session pod in this node pool cannot be placed.",
	}, []string{"node_pool"})
	CapacityEvictions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_capacity_evictions_total",
		Help: "Idle pods deleted to make room for a session.",
	}, []string{"node_pool"})
	Sessions = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_sessions",
		Help: "Sessions by template and phase.",
	}, []string{"template", "phase"})
	Workspaces = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_workspaces",
		Help: "Workspaces by phase.",
	}, []string{"phase"})
	AgentRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_agent_requests_total",
		Help: "Requests of the operator to agents by operation and result.",
	}, []string{"op", "result"})

	APIRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_api_requests_total",
		Help: "REST requests by operation and HTTP status code.",
	}, []string{"operation", "code"})
	APIRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_api_request_seconds",
		Help:    "REST request duration by operation (SSE streams excluded).",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation"})
	Launches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_launch_total",
		Help: "Launch requests by template and result: created, existing, rejected.",
	}, []string{"template", "result"})

	AuthzDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_authz_decisions_total",
		Help: "ext_authz decisions by result (allow, deny, login) and reason.",
	}, []string{"result", "reason"})
	AuthzDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "eduide_authz_check_seconds",
		Help:    "Duration of one ext_authz check.",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.05},
	})
)

// OperatorMetrics, ServiceMetrics and AuthzMetrics are registered by the binaries.
var (
	OperatorMetrics = []prometheus.Collector{SessionStartup, SessionFailures, SessionPhase, ClaimTotal, PoolInstances, PoolTargetIdle, CapacityPressure, CapacityEvictions, Sessions, Workspaces, AgentRequests}
	ServiceMetrics  = []prometheus.Collector{APIRequests, APIRequestDuration, Launches}
	AuthzMetrics    = []prometheus.Collector{AuthzDecisions, AuthzDuration}
)

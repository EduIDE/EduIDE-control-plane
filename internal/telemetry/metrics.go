package telemetry

import "github.com/prometheus/client_golang/prometheus"

// Metrics are a contract (docs/contracts/metrics.md); the pool scaler and the
// dashboards depend on names and labels. Change them only additively.
var (
	SessionStartup = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_session_startup_seconds",
		Help:    "Time from IDESession creation until phase Ready or Failed.",
		Buckets: []float64{0.25, 0.5, 1, 1.5, 2, 3, 4, 5, 6, 8, 10, 15, 20, 30, 45, 60, 120},
	}, []string{"template", "strategy", "result", "persistence", "start_mode"})
	SessionFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_session_failures_total",
		Help: "Sessions that ended in phase Failed by template and stable error code.",
	}, []string{"template", "code"})
	SessionPhase = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_session_phase_seconds",
		Help:    "Duration of one phase of the start path.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"phase", "strategy", "template"})
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
	PoolMinIdle = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_pool_min_idle",
		Help: "Configured idle floor of a pool (spec.minIdle) and the template it serves.",
	}, []string{"pool", "template"})
	PoolMaxInstances = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_pool_max_instances",
		Help: "Configured cap of idle plus claimed instances of a pool (spec.maxInstances) and the template it serves.",
	}, []string{"pool", "template"})
	SessionLifetime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_session_lifetime_seconds",
		Help:    "Time from IDESession creation until the session is removed, by reason: timeout, error, user.",
		Buckets: []float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400},
	}, []string{"template", "reason"})
	TemplateResources = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_template_resources",
		Help: "CPU (cores) and memory (bytes) request and limit of the IDE container of a template.",
	}, []string{"template", "resource", "kind"})
	TemplateTimeout = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "eduide_template_timeout_seconds",
		Help: "Resolved session timeouts of a template in seconds: idle, warning, max.",
	}, []string{"template", "kind"})
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
	GitProxyRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_gitproxy_requests_total",
		Help: "Git requests of the git-proxy by provider, kind (read, push) and result.",
	}, []string{"provider", "kind", "result"})
	GitProxyBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eduide_gitproxy_bytes_total",
		Help: "Bytes streamed by the git-proxy by provider and direction (in: client to provider, out: provider to client).",
	}, []string{"provider", "direction"})
	GitProxyUpstreamDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eduide_gitproxy_upstream_seconds",
		Help:    "Duration of a request to the provider host, until the last byte went to the client.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"provider", "kind"})
	AuthzDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "eduide_authz_check_seconds",
		Help:    "Duration of one ext_authz check.",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.05},
	})
)

// OperatorMetrics, ServiceMetrics and AuthzMetrics are registered by the binaries.
var (
	OperatorMetrics = []prometheus.Collector{SessionStartup, SessionFailures, SessionPhase, ClaimTotal, PoolInstances, PoolTargetIdle, PoolMinIdle, PoolMaxInstances, SessionLifetime, TemplateResources, TemplateTimeout, CapacityPressure, CapacityEvictions, Sessions, Workspaces, AgentRequests}
	ServiceMetrics  = []prometheus.Collector{APIRequests, APIRequestDuration, Launches}
	AuthzMetrics    = []prometheus.Collector{AuthzDecisions, AuthzDuration}
	GitProxyMetrics = []prometheus.Collector{GitProxyRequests, GitProxyBytes, GitProxyUpstreamDuration}
)

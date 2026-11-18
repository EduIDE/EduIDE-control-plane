package telemetry

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestMetricsContract keeps docs/contracts/metrics.md and the code in sync.
func TestMetricsContract(t *testing.T) {
	doc, err := os.ReadFile("../../docs/contracts/metrics.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `(eduide_[a-z_]+)`").FindAllStringSubmatch(string(doc), -1) {
		documented[m[1]] = true
	}
	reg := prometheus.NewRegistry()
	all := slices.Concat(OperatorMetrics, ServiceMetrics, AuthzMetrics)
	reg.MustRegister(all...)
	// Touch every vector so Gather reports it.
	SessionStartup.WithLabelValues("t", "cold", "ready")
	SessionPhase.WithLabelValues("claim", "cold")
	ClaimTotal.WithLabelValues("t", "claimed")
	PoolInstances.WithLabelValues("p", "idle_ready")
	PoolTargetIdle.WithLabelValues("p")
	CapacityPressure.WithLabelValues("n")
	CapacityEvictions.WithLabelValues("n")
	Sessions.WithLabelValues("t", "Ready")
	Workspaces.WithLabelValues("Ready")
	AgentRequests.WithLabelValues("claim", "ok")
	APIRequests.WithLabelValues("launchSession", "201")
	APIRequestDuration.WithLabelValues("launchSession")
	Launches.WithLabelValues("t", "created")
	AuthzDecisions.WithLabelValues("allow", "owner")
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	inCode := map[string]bool{}
	for _, mf := range mfs {
		inCode[mf.GetName()] = true
		if !documented[mf.GetName()] {
			t.Errorf("metric %s is not in docs/contracts/metrics.md", mf.GetName())
		}
	}
	for name := range documented {
		if !inCode[name] {
			t.Errorf("documented metric %s does not exist in code", name)
		}
	}
}

func TestUserIDIsStableAndSalted(t *testing.T) {
	a, b := UserID([]byte("s1"), "alice"), UserID([]byte("s2"), "alice")
	if a == b || a != UserID([]byte("s1"), "alice") || len(a) != 16 {
		t.Fatalf("unexpected ids %s %s", a, b)
	}
}

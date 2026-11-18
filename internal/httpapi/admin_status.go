package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	// poolEmptyWarnAfter keeps the short gap after a claim from raising a warning.
	poolEmptyWarnAfter = time.Minute
	problemWindow      = time.Hour
	maxProblemEvents   = 200
	scalerController   = "pool-scaler"
)

// eventTime is when an event happened, for events of both API shapes (events.k8s.io and the older core ones).
func eventTime(e *eventsv1.Event) time.Time {
	t := e.EventTime.Time
	if e.Series != nil && e.Series.LastObservedTime.After(t) {
		t = e.Series.LastObservedTime.Time
	}
	for _, c := range []time.Time{e.DeprecatedLastTimestamp.Time, e.DeprecatedFirstTimestamp.Time, e.CreationTimestamp.Time} {
		if t.IsZero() {
			t = c
		}
	}
	return t
}

func (s *Server) events(ctx context.Context) ([]eventsv1.Event, *apiError) {
	var l eventsv1.EventList
	if err := s.r.List(ctx, &l, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	return l.Items, nil
}

// scalerDecisions are the events of the pool scaler on IDEPools, newest first.
func (s *Server) scalerDecisions(ctx context.Context) ([]AdminDecision, *apiError) {
	evs, perr := s.events(ctx)
	if perr != nil {
		return nil, perr
	}
	out := []AdminDecision{}
	for i := range evs {
		e := &evs[i]
		if e.ReportingController != scalerController || e.Regarding.Kind != "IDEPool" {
			continue
		}
		out = append(out, AdminDecision{Time: eventTime(e), Pool: e.Regarding.Name, Reason: AdminDecisionReason(e.Reason), Message: truncate(e.Note, 500)})
	}
	slices.SortStableFunc(out, func(a, b AdminDecision) int { return b.Time.Compare(a.Time) })
	return out, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var errCodeRE = regexp.MustCompile(`^([a-z][a-z-]{2,40}): `)

// problemEvents are the Warning events on sessions of the last hour, newest first.
func (s *Server) problemEvents(ctx context.Context) ([]AdminProblemEvent, *apiError) {
	evs, perr := s.events(ctx)
	if perr != nil {
		return nil, perr
	}
	sessions, perr := s.allSessions(ctx)
	if perr != nil {
		return nil, perr
	}
	byName := map[string]*v1alpha1.IDESession{}
	for i := range sessions {
		byName[sessions[i].Name] = &sessions[i]
	}
	cutoff := s.now().Add(-problemWindow)
	out := []AdminProblemEvent{}
	for i := range evs {
		e := &evs[i]
		if e.Type != corev1.EventTypeWarning || e.Regarding.Kind != "IDESession" || eventTime(e).Before(cutoff) {
			continue
		}
		pe := AdminProblemEvent{Time: eventTime(e), Reason: e.Reason, Message: truncate(e.Note, 500), Session: emptyToNil(e.Regarding.Name)}
		if m := errCodeRE.FindStringSubmatch(e.Note); m != nil {
			pe.Code = ptr(m[1])
			pe.Message = truncate(strings.TrimPrefix(e.Note, m[0]), 500)
		}
		if sess := byName[e.Regarding.Name]; sess != nil {
			a := s.adminSessionAPI(sess, nil)
			pe.Template, pe.OwnerHash, pe.TraceId, pe.TraceUrl = &a.Template, &a.OwnerHash, a.TraceId, a.TraceUrl
		}
		out = append(out, pe)
	}
	slices.SortStableFunc(out, func(a, b AdminProblemEvent) int { return b.Time.Compare(a.Time) })
	return out[:min(len(out), maxProblemEvents)], nil
}

func (s *Server) GetAdminProblems(w http.ResponseWriter, r *http.Request, p GetAdminProblemsParams) {
	since := GetAdminProblemsParamsSince("24h")
	if p.Since != nil {
		if since = *p.Since; !since.Valid() {
			writeProblem(w, problem(CodeInvalidInput, "since must be 1h or 24h"))
			return
		}
	}
	evs, perr := s.problemEvents(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	out := AdminProblems{Since: AdminProblemsSince(since), Events: evs}
	if s.prom != nil {
		// since is one of the two enum values, so the query text is fixed.
		q := fmt.Sprintf(`sum by (code) (increase(eduide_session_failures_total{%s}[%s]))`, s.promNS(), since)
		if res, err := s.prom.Instant(r.Context(), q); err == nil {
			codes := []AdminCodeCount{}
			for _, x := range res {
				if n := int(math.Round(x.Value)); n > 0 {
					codes = append(codes, AdminCodeCount{Code: x.Labels["code"], Count: n})
				}
			}
			slices.SortFunc(codes, func(a, b AdminCodeCount) int { return b.Count - a.Count })
			out.Codes = &codes
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- status ---

func (s *Server) adminStatus(ctx context.Context) (AdminStatus, *apiError) {
	sessions, perr := s.allSessions(ctx)
	if perr != nil {
		return AdminStatus{}, perr
	}
	ps, perr := s.pools(ctx)
	if perr != nil {
		return AdminStatus{}, perr
	}
	now := s.now()
	out := AdminStatus{Warnings: []AdminWarning{}}
	for i := range sessions {
		switch sessions[i].Status.Phase {
		case v1alpha1.SessionReady:
			out.RunningSessions++
		case v1alpha1.SessionFailed:
		default:
			out.StartingSessions++
		}
	}
	for i := range ps {
		p := &ps[i]
		out.IdleReady += int(p.Status.IdleReady)
		out.IdleTarget += int(effectiveTarget(p))
		if poolPressure(p) {
			out.CapacityPressure = true
			out.Warnings = append(out.Warnings, AdminWarning{Code: CapacityPressure, Pool: ptr(p.Name),
				Message: fmt.Sprintf("Pool %s is under capacity pressure: the operator keeps no idle instances.", p.Name)})
		}
		if since, ok := s.hub.poolEmptySince(p.Name); ok && poolEmpty(p) && now.Sub(since) >= poolEmptyWarnAfter {
			out.Warnings = append(out.Warnings, AdminWarning{Code: PoolEmpty, Pool: ptr(p.Name), Since: &since,
				Message: fmt.Sprintf("Pool %s has had no free instance for %d min.", p.Name, int(now.Sub(since).Minutes()))})
		}
	}
	if s.prom != nil {
		if w := s.scalerSilent(ctx, len(ps) > 0); w != nil {
			out.Warnings = append(out.Warnings, *w)
		}
		out.Stats, out.NodePools = s.promStats(ctx)
	}
	return out, nil
}

// promNS is the PromQL matcher of this deployment's namespace (the PodMonitor sets the namespace label).
func (s *Server) promNS() string { return "namespace=" + strconv.Quote(s.cfg.Namespace) }

// scalerSilent warns when a pool scaler is configured (admin.scalerInterval) but Prometheus has no recent decision
// round of it: its heartbeat pool_scaler_last_decision_timestamp_seconds is older than two intervals, or absent
// because the target is down or not scraped. Events cannot tell, a steady pool writes none. A failed query warns
// about nothing.
func (s *Server) scalerSilent(ctx context.Context, havePools bool) *AdminWarning {
	iv := s.cfg.Admin.ScalerInterval
	if iv == nil || !havePools {
		return nil
	}
	res, err := s.prom.Instant(ctx, `time() - max(pool_scaler_last_decision_timestamp_seconds{`+s.promNS()+`})`)
	if err != nil {
		return nil
	}
	limit := 2 * iv.Duration
	if len(res) == 0 {
		return &AdminWarning{Code: ScalerSilent, Message: "Prometheus has no metrics of the pool scaler: it is down or not scraped."}
	}
	age := time.Duration(res[0].Value * float64(time.Second))
	if age <= limit {
		return nil
	}
	since := s.now().Add(-age)
	return &AdminWarning{Code: ScalerSilent, Since: &since, Message: fmt.Sprintf("The pool scaler made no decision round for more than %s.", limit)}
}

func (s *Server) GetAdminStatus(w http.ResponseWriter, r *http.Request) {
	out, perr := s.adminStatus(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) WatchAdminStatus(w http.ResponseWriter, r *http.Request) {
	s.streamEvery(w, r, adminKey, "status", adminEventGap, func(ctx context.Context) (any, bool, *apiError) {
		out, perr := s.adminStatus(ctx)
		return out, false, perr
	})
}

// promStats runs the status queries side by side. A query that fails leaves its fields absent; when all fail
// the whole block is absent.
func (s *Server) promStats(ctx context.Context) (*AdminStats, *[]NodePoolPressure) {
	const hour = time.Hour
	var (
		st   AdminStats
		pres []NodePoolPressure
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   bool
	)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := f()
			mu.Lock()
			ok = ok || err == nil
			mu.Unlock()
		}()
	}
	scalar := func(q string, set func(float64)) func() error {
		return func() error {
			res, err := s.prom.Instant(ctx, q)
			if err == nil && len(res) > 0 {
				set(res[0].Value)
			}
			return err
		}
	}
	// Every eduide_ series is limited to this deployment: other deployments in the cluster report the same names.
	ns := s.promNS()
	startCount := `eduide_session_startup_seconds_count{` + ns + `,`
	run(scalar(`sum(increase(`+startCount+`result="ready"}[1h]))`, func(v float64) { st.StartsLastHour = ptr(int(math.Round(v))) }))
	run(scalar(`sum(increase(eduide_session_failures_total{`+ns+`}[1h]))`, func(v float64) { st.FailuresLastHour = ptr(int(math.Round(v))) }))
	run(scalar(`100 * sum(kube_pod_container_resource_requests{resource="cpu"}) / sum(kube_node_status_allocatable{resource="cpu"})`, func(v float64) { st.CpuRequestsPercent = &v }))
	run(scalar(`count(kube_node_info)`, func(v float64) { st.NodeCount = ptr(int(v)) }))
	run(func() error {
		res, err := s.prom.Instant(ctx, `histogram_quantile(0.95, sum by (le, template, persistence) (increase(eduide_session_startup_seconds_bucket{`+ns+`,result="ready"}[1h])))`)
		var p95 []AdminStartP95
		for _, x := range res {
			p95 = append(p95, AdminStartP95{Template: x.Labels["template"], Persistence: PersistenceMode(x.Labels["persistence"]), Seconds: x.Value})
		}
		slices.SortFunc(p95, func(a, b AdminStartP95) int {
			return strings.Compare(a.Template+"/"+string(a.Persistence), b.Template+"/"+string(b.Persistence))
		})
		if p95 != nil {
			st.StartP95 = &p95
		}
		return err
	})
	run(func() error {
		res, err := s.prom.Instant(ctx, `max by (node_pool) (eduide_capacity_pressure{`+ns+`})`)
		for _, x := range res {
			pres = append(pres, NodePoolPressure{Name: x.Labels["node_pool"], Pressure: x.Value > 0})
		}
		slices.SortFunc(pres, func(a, b NodePoolPressure) int { return strings.Compare(a.Name, b.Name) })
		return err
	})
	run(func() error { // starts per hour: three series on the same hourly timestamps
		var series [3][]promSeries
		var firstErr error
		for i, q := range []string{
			`sum(increase(` + startCount + `result="ready",strategy=~"warm.*"}[1h]))`,
			`sum(increase(` + startCount + `result="ready",strategy="cold"}[1h]))`,
			`sum(increase(` + startCount + `result="failed"}[1h]))`,
		} {
			res, err := s.prom.Range(ctx, q, 12*hour, hour)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			series[i] = res
		}
		byTime := map[time.Time]*AdminStartsBucket{}
		var times []time.Time
		for i := range series {
			for _, sr := range series[i] {
				for _, p := range sr.Points {
					b := byTime[p.T]
					if b == nil {
						b = &AdminStartsBucket{Time: p.T}
						byTime[p.T] = b
						times = append(times, p.T)
					}
					*[]*float64{&b.Warm, &b.Cold, &b.Failed}[i] = math.Round(p.V)
				}
			}
		}
		slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
		var out []AdminStartsBucket
		for _, t := range times {
			out = append(out, *byTime[t])
		}
		if out != nil {
			st.StartsPerHour = &out
		}
		return firstErr
	})
	run(func() error {
		byTime := map[time.Time]*AdminLatencyPoint{}
		var times []time.Time
		var firstErr error
		for i, q := range []float64{0.5, 0.95} {
			res, err := s.prom.Range(ctx, fmt.Sprintf(`histogram_quantile(%g, sum by (le) (increase(eduide_session_startup_seconds_bucket{%s,result="ready"}[1h])))`, q, ns), 12*hour, hour)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			for _, sr := range res {
				for _, p := range sr.Points {
					b := byTime[p.T]
					if b == nil {
						b = &AdminLatencyPoint{Time: p.T}
						byTime[p.T] = b
						times = append(times, p.T)
					}
					v := p.V
					if i == 0 {
						b.P50 = &v
					} else {
						b.P95 = &v
					}
				}
			}
		}
		slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
		var out []AdminLatencyPoint
		for _, t := range times {
			out = append(out, *byTime[t])
		}
		if out != nil {
			st.StartLatency = &out
		}
		return firstErr
	})
	run(func() error {
		res, err := s.prom.Range(ctx, `sum by (pool) (eduide_pool_instances{`+ns+`,state="idle_ready"})`, 6*hour, 15*time.Minute)
		var free []AdminPoolFree
		for _, sr := range res {
			pf := AdminPoolFree{Pool: sr.Labels["pool"], Points: []TimePoint{}}
			for _, p := range sr.Points {
				pf.Points = append(pf.Points, TimePoint{Time: p.T, Value: p.V})
			}
			free = append(free, pf)
		}
		if free != nil {
			st.PoolFree = &free
		}
		return err
	})
	wg.Wait()
	if !ok {
		return nil, nil
	}
	var np *[]NodePoolPressure
	if pres != nil {
		np = &pres
	}
	return &st, np
}

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	schemaVersion = 2
	// minCell is the smallest number of sessions in a cell that may carry a distribution.
	minCell = 10
)

// Row is one line of weekly.csv. NaN means "empty cell".
type Row struct {
	Week, WeekStart, Metric, Template, Dimension, Unit string
	Days                                               int
	Count                                              float64
	Suppressed                                         bool
	Value, P50, P95, Max, FailureShare                 float64
	PeakHour, PeakMinute, Max20s, Max60s               float64
	// noCell marks rows that are not per-session figures (pool hours, restarts); they need no count.
	noCell bool
	// distCount is the number of sessions the distribution columns come from, when it differs from Count.
	distCount float64
}

var csvHeader = []string{"week", "week_start", "days", "metric", "template", "dimension", "unit", "count", "suppressed", "value", "p50", "p95", "max", "failure_share", "peak_hour", "peak_minute", "max_20s", "max_60s"}

func nan() float64 { return math.NaN() }

func newRow(w window, metric, template, dim, unit string) *Row {
	return &Row{Week: w.label(), WeekStart: w.start.Format("2006-01-02"), Days: w.days(), Metric: metric, Template: template, Dimension: dim, Unit: unit,
		Count: nan(), distCount: nan(), Value: nan(), P50: nan(), P95: nan(), Max: nan(), FailureShare: nan(), PeakHour: nan(), PeakMinute: nan(), Max20s: nan(), Max60s: nan()}
}

// suppress applies the small-cell rule: below minCell sessions only the count stays.
// A missing count counts as small, except for rows that are not per-session figures.
func (r *Row) suppress() {
	if r.noCell {
		return
	}
	clearAll := math.IsNaN(r.Count) || r.Count < minCell
	clearDist := !math.IsNaN(r.distCount) && r.distCount < minCell
	if !clearAll && !clearDist {
		return
	}
	r.Suppressed = true
	r.P50, r.P95, r.Max = nan(), nan(), nan()
	if clearAll {
		r.Value, r.FailureShare = nan(), nan()
		r.PeakHour, r.PeakMinute, r.Max20s, r.Max60s = nan(), nan(), nan(), nan()
	}
}

func f(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', 6, 64)
}

func writeCSV(path string, rows []*Row) error {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return fmt.Sprint(a.WeekStart, "|", a.Metric, "|", a.Template, "|", a.Dimension) < fmt.Sprint(b.WeekStart, "|", b.Metric, "|", b.Template, "|", b.Dimension)
	})
	fh, err := os.Create(path) //nolint:gosec // path is built from validated flags
	if err != nil {
		return err
	}
	w := csv.NewWriter(fh)
	_ = w.Write(csvHeader)
	for _, r := range rows {
		_ = w.Write([]string{r.Week, r.WeekStart, strconv.Itoa(r.Days), r.Metric, r.Template, r.Dimension, r.Unit, f(r.Count), strconv.FormatBool(r.Suppressed),
			f(r.Value), f(r.P50), f(r.P95), f(r.Max), f(r.FailureShare), f(r.PeakHour), f(r.PeakMinute), f(r.Max20s), f(r.Max60s)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

// window is the part of one ISO week inside the requested period.
type window struct{ start, end time.Time }

func (w window) label() string { y, wk := w.start.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, wk) }
func (w window) days() int     { return int(math.Round(w.end.Sub(w.start).Hours() / 24)) }
func (w window) dur() string   { return fmt.Sprintf("%ds", int(math.Ceil(w.end.Sub(w.start).Seconds()))) }

// weeks splits [from, end) at ISO week boundaries (Monday, UTC).
func weeks(from, end time.Time) []window {
	var out []window
	monday := from.AddDate(0, 0, -((int(from.Weekday()) + 6) % 7))
	for ; monday.Before(end); monday = monday.AddDate(0, 0, 7) {
		s, e := monday, monday.AddDate(0, 0, 7)
		if s.Before(from) {
			s = from
		}
		if e.After(end) {
			e = end
		}
		if e.After(s) {
			out = append(out, window{s, e})
		}
	}
	return out
}

// histogram is a cumulative bucket list of one group.
type histogram struct {
	les []float64
	cum []float64
}

func (h *histogram) total() float64 {
	if len(h.cum) == 0 {
		return 0
	}
	return h.cum[len(h.cum)-1]
}

// quantile follows histogram_quantile of Prometheus.
func (h *histogram) quantile(q float64) float64 {
	t := h.total()
	if t <= 0 {
		return nan()
	}
	rank := q * t
	for i, c := range h.cum {
		if c < rank {
			continue
		}
		if math.IsInf(h.les[i], 1) {
			if i == 0 {
				return nan()
			}
			return h.les[i-1]
		}
		lo, prev := 0.0, 0.0
		if i > 0 {
			lo, prev = h.les[i-1], h.cum[i-1]
		}
		if c == prev {
			return h.les[i]
		}
		return lo + (h.les[i]-lo)*(rank-prev)/(c-prev)
	}
	return nan()
}

// maxBucket is the upper bound of the highest bucket that holds an observation.
func (h *histogram) maxBucket() float64 {
	prev := 0.0
	max := nan()
	for i, c := range h.cum {
		if c > prev+1e-9 {
			max = h.les[i]
		}
		prev = c
	}
	return max
}

// histograms groups a bucket query by the given labels. Buckets are made cumulative and monotonic,
// because a counter reset can hit the buckets of one window unevenly.
func histograms(v []sample, group []string) map[[2]string]*histogram {
	out := map[[2]string]*histogram{}
	for _, s := range v {
		le, err := strconv.ParseFloat(s.Labels["le"], 64)
		if err != nil {
			continue
		}
		k := [2]string{label(s, group, 0), label(s, group, 1)}
		h := out[k]
		if h == nil {
			h = &histogram{}
			out[k] = h
		}
		h.les = append(h.les, le)
		h.cum = append(h.cum, s.Value)
	}
	for _, h := range out {
		idx := make([]int, len(h.les))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return h.les[idx[a]] < h.les[idx[b]] })
		les, cum := make([]float64, len(idx)), make([]float64, len(idx))
		run := 0.0
		for i, j := range idx {
			les[i], run = h.les[j], math.Max(run, h.cum[j])
			cum[i] = run
		}
		h.les, h.cum = les, cum
	}
	return out
}

// label returns the value of the i-th group label; an absent label reads "unknown" (metric not deployed with it yet).
func label(s sample, group []string, i int) string {
	if i >= len(group) {
		return ""
	}
	if v := s.Labels[group[i]]; v != "" {
		return v
	}
	return "unknown"
}

// collector runs the allowlist for one installation.
type collector struct {
	p       *prom
	ns      string
	scrape  time.Duration
	rows    []*Row
	lastErr error
}

func (c *collector) q(ctx context.Context, id, expr string, at time.Time) []sample {
	v, err := c.p.query(ctx, id, expr, at)
	if err != nil && c.lastErr == nil {
		c.lastErr = err
	}
	return v
}

func (c *collector) sel(extra string) string {
	if extra != "" {
		extra = ", " + extra
	}
	return fmt.Sprintf(`{namespace=%q%s}`, c.ns, extra)
}

// controlPlane selects the scrape targets of the control plane, not the session pods or other workloads.
func (c *collector) controlPlane() string {
	return fmt.Sprintf(`{namespace=%q, container=~"operator|service|authz"}`, c.ns)
}

func (c *collector) add(r *Row) { r.suppress(); c.rows = append(c.rows, r) }

// rec selects a recorded series of usage-rules.yaml in the namespace.
func (c *collector) rec(name, extra string) string { return "eduide:usage:" + name + c.sel(extra) }

// collectWeek adds the rows of one window. Every number comes from the recording rules of the chart
// (files/usage-rules.yaml); the export only sums, maximises or takes quantiles of the recorded series over the window.
// Every expression is a fixed template; only the validated namespace and the window length are filled in.
func (c *collector) collectWeek(ctx context.Context, w window) {
	c.p.week = w.label()
	defer func() { c.p.week = "" }()
	d, at := w.dur(), w.end

	launched := c.launches(ctx, w, d, at)
	c.startup(ctx, w, d, at)
	c.running(ctx, w, d, at, launched)
	for _, h := range []struct{ id, rec, dim, metric string }{
		{"m4_phase", "phase_bucket_5m", "phase", "phase"},
		{"m6_lifetime", "session_lifetime_bucket_5m", "reason", "lifetime"},
	} {
		b := c.q(ctx, h.id, fmt.Sprintf("sum by (le, template, %s) (sum_over_time(%s[%s]))", h.dim, c.rec(h.rec, ""), d), at)
		for k, hist := range histograms(b, []string{"template", h.dim}) {
			r := newRow(w, h.metric, k[0], k[1], "seconds")
			r.Count, r.P50, r.P95, r.Max = hist.total(), hist.quantile(0.5), hist.quantile(0.95), hist.maxBucket()
			c.add(r)
		}
	}
	c.agentClaim(ctx, w, d, at)
	c.pool(ctx, w, d, at, launched)
	c.resources(ctx, w, d, at)
}

// launches is M1: launch counts and the largest counts inside a clock hour, a minute and 20 s and 60 s windows,
// per template and for the whole installation (template "all").
func (c *collector) launches(ctx context.Context, w window, d string, at time.Time) map[string]*Row {
	rows := map[string]*Row{}
	lpm := c.rec("launches_per_minute", "")
	for _, all := range []bool{false, true} {
		by, suffix := "sum by (template)", ""
		if all {
			by, suffix = "sum", "_all"
		}
		maxBy := strings.Replace(by, "sum", "max", 1)
		stats := []struct {
			id, expr string
			set      func(*Row, float64)
		}{
			{"m1_launches", fmt.Sprintf("%s (sum_over_time(%s[%s]))", by, lpm, d), func(r *Row, v float64) { r.Count = v }},
			{"m1_peak_minute", fmt.Sprintf("max_over_time(%s (%s)[%s:1m])", by, lpm, d), func(r *Row, v float64) { r.PeakMinute = v }},
			{"m1_peak_hour", fmt.Sprintf("max_over_time(%s (sum_over_time(%s[1h]))[%s:1h])", by, lpm, d), func(r *Row, v float64) { r.PeakHour = v }},
		}
		if c.resolves(20 * time.Second) {
			stats = append(stats, struct {
				id, expr string
				set      func(*Row, float64)
			}{"m1_max_20s", fmt.Sprintf("%s (max_over_time(%s[%s]))", maxBy, c.rec("launches_max_20s_in_minute"+suffix, ""), d), func(r *Row, v float64) { r.Max20s = v }})
		}
		if c.resolves(60 * time.Second) {
			stats = append(stats, struct {
				id, expr string
				set      func(*Row, float64)
			}{"m1_max_60s", fmt.Sprintf("%s (max_over_time(%s[%s]))", maxBy, c.rec("launches_max_60s_in_minute"+suffix, ""), d), func(r *Row, v float64) { r.Max60s = v }})
		}
		for _, st := range stats {
			for _, s := range c.q(ctx, st.id, st.expr, at) {
				t := "all"
				if !all {
					t = label(s, []string{"template"}, 0)
				}
				if rows[t] == nil {
					rows[t] = newRow(w, "launches", t, "created", "launches")
				}
				st.set(rows[t], s.Value)
			}
		}
	}
	for _, r := range rows {
		c.add(r)
	}
	return rows
}

// resolves tells whether a window holds at least two scrapes.
func (c *collector) resolves(win time.Duration) bool { return c.scrape > 0 && win >= 2*c.scrape }

// startup is M2 and the lazy fallback and not-ready claim shares of M3.
func (c *collector) startup(ctx context.Context, w window, d string, at time.Time) {
	counts := c.q(ctx, "m2_startup_count", fmt.Sprintf("sum by (template, start_mode, result, strategy) (sum_over_time(%s[%s]))", c.rec("startup_count_5m", ""), d), at)
	total, failed, perTemplate, fallback, hasMode := map[[2]string]float64{}, map[[2]string]float64{}, map[string]float64{}, map[string]float64{}, map[string]bool{}
	warm, warmStarting := map[string]float64{}, map[string]float64{}
	for _, s := range counts {
		k := [2]string{label(s, []string{"template"}, 0), label(s, []string{"start_mode"}, 0)}
		total[k] += s.Value
		perTemplate[k[0]] += s.Value
		if s.Labels["result"] == "failed" {
			failed[k] += s.Value
		}
		switch s.Labels["strategy"] {
		case "warm-ready":
			warm[k[0]] += s.Value
		case "warm-starting":
			warm[k[0]] += s.Value
			warmStarting[k[0]] += s.Value
		}
		hasMode[k[0]] = hasMode[k[0]] || k[1] != "unknown"
		if k[1] == "lazy_fallback" {
			fallback[k[0]] += s.Value
		}
	}
	b := c.q(ctx, "m2_startup_bucket", fmt.Sprintf("sum by (le, template, start_mode) (sum_over_time(%s[%s]))", c.rec("startup_bucket_5m", `result="ready"`), d), at)
	hs := histograms(b, []string{"template", "start_mode"})
	for k, n := range total {
		r := newRow(w, "startup", k[0], k[1], "seconds")
		r.Count = n
		if n > 0 {
			r.FailureShare = failed[k] / n
		}
		if h := hs[k]; h != nil {
			// The quantiles come from ready sessions only, so the small-cell rule checks that count too.
			r.distCount = h.total()
			r.P50, r.P95, r.Max = h.quantile(0.5), h.quantile(0.95), h.maxBucket()
		} else {
			r.distCount = 0
		}
		c.add(r)
	}
	for t, n := range warm {
		r := newRow(w, "not_ready_claim_share", t, "warm-starting", "share")
		r.Count = n
		if n > 0 {
			r.Value = warmStarting[t] / n
		}
		c.add(r)
	}
	for t, n := range perTemplate {
		if !hasMode[t] {
			continue // the metric has no start_mode label yet
		}
		r := newRow(w, "lazy_fallback_share", t, "lazy_fallback", "share")
		r.Count = n
		if n > 0 {
			r.Value = fallback[t] / n
		}
		c.add(r)
	}
}

// running is the concurrency part of M1: p50, p95 and max over the per-minute maxima of the recorded gauge.
// The count of a row is the launch count of the same template in the week.
func (c *collector) running(ctx context.Context, w window, d string, at time.Time, launched map[string]*Row) {
	for _, all := range []bool{false, true} {
		series := c.rec("running_sessions_max_1m", "")
		if all {
			series = c.rec("running_sessions_all_max_1m", "")
		}
		stats := map[string]*Row{}
		for _, st := range []struct {
			fn  string
			set func(*Row, float64)
		}{
			{"quantile_over_time(0.5, %s[%s])", func(r *Row, v float64) { r.P50 = v }},
			{"quantile_over_time(0.95, %s[%s])", func(r *Row, v float64) { r.P95 = v }},
			{"max_over_time(%s[%s])", func(r *Row, v float64) { r.Max = v }},
		} {
			for _, s := range c.q(ctx, "m1_running", fmt.Sprintf(st.fn, series, d), at) {
				t := "all"
				if !all {
					t = label(s, []string{"template"}, 0)
				}
				if stats[t] == nil {
					stats[t] = newRow(w, "running_sessions", t, "Ready", "sessions")
					if l := launched[t]; l != nil {
						stats[t].Count = l.Count
					}
				}
				st.set(stats[t], s.Value)
			}
		}
		for _, r := range stats {
			c.add(r)
		}
	}
}

func (c *collector) agentClaim(ctx context.Context, w window, d string, at time.Time) {
	all := c.q(ctx, "m4_agent_claim", fmt.Sprintf("sum (sum_over_time(%s[%s]))", c.rec("agent_claim_requests_5m", ""), d), at)
	bad := c.q(ctx, "m4_agent_claim_errors", fmt.Sprintf("sum (sum_over_time(%s[%s]))", c.rec("agent_claim_errors_5m", ""), d), at)
	if len(all) == 0 {
		return
	}
	r := newRow(w, "agent_claim", "all", "error", "requests")
	r.Count = all[0].Value
	if all[0].Value > 0 {
		r.FailureShare = 0
		if len(bad) > 0 {
			r.FailureShare = bad[0].Value / all[0].Value
		}
	}
	c.add(r)
}

// pool is M3: slot-hours by state and the hours without an unclaimed slot. The rule group evaluates every
// minute, so a sample is one minute.
// Hours of claimed slots and of empty pools follow the load, so they are suppressed like a distribution when the
// template had fewer than 10 sessions in the week; hours of idle slots do not depend on sessions.
func (c *collector) pool(ctx context.Context, w window, d string, at time.Time, launched map[string]*Row) {
	sessions := func(r *Row, t string) {
		r.Count = nan()
		if l := launched[t]; l != nil {
			r.Count = l.Count
		}
	}
	for _, s := range c.q(ctx, "m3_pool_slot_hours", fmt.Sprintf(`sum by (template, state) (sum_over_time(%s[%s])) / 60`, c.rec("pool_instances", ""), d), at) {
		t := label(s, []string{"template"}, 0)
		r := newRow(w, "pool_slot_hours", t, s.Labels["state"], "slot_hours")
		r.Value = s.Value
		if s.Labels["state"] == "claimed" {
			sessions(r, t)
		} else {
			r.noCell = true
		}
		c.add(r)
	}
	for _, s := range c.q(ctx, "m3_pool_empty_hours", fmt.Sprintf(`sum by (template) (sum_over_time(%s[%s])) / 60`, c.rec("pool_unclaimed_empty", ""), d), at) {
		t := label(s, []string{"template"}, 0)
		r := newRow(w, "pool_empty_hours", t, "no_unclaimed", "hours")
		r.Value = s.Value
		sessions(r, t)
		c.add(r)
	}
}

// resources is M5 and M7. It reads the recorded series: the per-pod series are not exported.
// Rows with the template and state split come from kube_pod_labels; without it the per-namespace fallback is used.
func (c *collector) resources(ctx context.Context, w window, d string, at time.Time) {
	for _, m := range []struct{ metric, rec, unit string }{{"memory", "ide_memory_bytes", "bytes"}, {"cpu", "ide_cpu_cores", "cores"}} {
		for _, suffix := range []string{"", "_all"} {
			by := "template, state"
			if suffix != "" {
				by = "namespace"
			}
			series := fmt.Sprintf("eduide:usage:%s%s", m.rec, suffix)
			pods := fmt.Sprintf("eduide:usage:ide_pods%s", suffix)
			rows := map[[2]string]*Row{}
			for _, st := range []struct {
				stat string
				set  func(*Row, float64)
				fn   string
			}{
				{"p50", func(r *Row, v float64) { r.P50 = v }, "quantile_over_time(0.5, %s:p50%s[%s])"},
				{"p95", func(r *Row, v float64) { r.P95 = v }, "quantile_over_time(0.95, %s:p95%s[%s])"},
				{"max", func(r *Row, v float64) { r.Max = v }, "max_over_time(%s:max%s[%s])"},
			} {
				if m.metric == "cpu" && st.stat == "max" {
					continue
				}
				v := c.q(ctx, "m5_"+m.metric+suffix, fmt.Sprintf("max by ("+by+") ("+st.fn+")", series, c.sel(""), d), at)
				for _, s := range v {
					k := [2]string{"all", "all"}
					if suffix == "" {
						k = [2]string{label(s, []string{"template"}, 0), label(s, []string{"state"}, 0)}
					}
					if rows[k] == nil {
						rows[k] = newRow(w, m.metric, k[0], k[1], m.unit)
					}
					st.set(rows[k], s.Value)
				}
			}
			for _, s := range c.q(ctx, "m5_pods"+suffix, fmt.Sprintf("max by ("+by+") (max_over_time(%s%s[%s]))", pods, c.sel(""), d), at) {
				k := [2]string{"all", "all"}
				if suffix == "" {
					k = [2]string{label(s, []string{"template"}, 0), label(s, []string{"state"}, 0)}
				}
				if r := rows[k]; r != nil {
					r.Count = s.Value
				}
			}
			if len(rows) > 0 {
				for _, r := range rows {
					c.add(r)
				}
				break // the labeled series win over the fallback
			}
		}
	}
	c.restarts(ctx, w, d, at)
}

// restarts is M7: container restarts and OOM kills of IDE pods per template.
func (c *collector) restarts(ctx context.Context, w window, d string, at time.Time) {
	restarts := c.q(ctx, "m7_restarts", fmt.Sprintf("sum by (template) (sum_over_time(%s[%s]))", c.rec("ide_restarts_5m", ""), d), at)
	kills := map[string]float64{}
	for _, s := range c.q(ctx, "m7_oom_kills", fmt.Sprintf("sum by (template) (sum_over_time(%s[%s]))", c.rec("ide_oom_kills_5m", ""), d), at) {
		kills[label(s, []string{"template"}, 0)] = s.Value
	}
	for _, s := range restarts {
		t := label(s, []string{"template"}, 0)
		r := newRow(w, "restarts", t, "ide_pods", "restarts")
		r.Value, r.noCell = math.Round(s.Value), true
		c.add(r)
		k := newRow(w, "oom_kills", t, "ide_pods", "oom_kills")
		k.Value, k.noCell = math.Round(kills[t]), true
		c.add(k)
	}
}

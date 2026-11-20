package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"
)

const (
	schemaVersion = 1
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
}

var csvHeader = []string{"week", "week_start", "days", "metric", "template", "dimension", "unit", "count", "suppressed", "value", "p50", "p95", "max", "failure_share", "peak_hour", "peak_minute", "max_20s", "max_60s"}

func nan() float64 { return math.NaN() }

func newRow(w window, metric, template, dim, unit string) *Row {
	return &Row{Week: w.label(), WeekStart: w.start.Format("2006-01-02"), Days: w.days(), Metric: metric, Template: template, Dimension: dim, Unit: unit,
		Count: nan(), Value: nan(), P50: nan(), P95: nan(), Max: nan(), FailureShare: nan(), PeakHour: nan(), PeakMinute: nan(), Max20s: nan(), Max60s: nan()}
}

// suppress applies the small-cell rule: below minCell sessions only the count stays.
func (r *Row) suppress() {
	if math.IsNaN(r.Count) || r.Count >= minCell {
		return
	}
	r.Suppressed = true
	r.Value, r.P50, r.P95, r.Max, r.FailureShare = nan(), nan(), nan(), nan(), nan()
	r.PeakHour, r.PeakMinute, r.Max20s, r.Max60s = nan(), nan(), nan(), nan()
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

// histograms groups a bucket query by the given labels. Buckets are made cumulative and monotonic
// because increase() extrapolates each bucket separately.
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

// collectWeek adds the rows of one window. Every expression is a fixed template; only the
// validated namespace and the window length are filled in.
func (c *collector) collectWeek(ctx context.Context, w window) {
	d, at := w.dur(), w.end
	hours := w.end.Sub(w.start).Hours()
	step := fmt.Sprintf("%ds", int(c.scrape.Seconds()))

	c.launches(ctx, w, d, at, step)
	c.startup(ctx, w, d, at)
	c.running(ctx, w, d, at)
	for _, h := range []struct{ id, base, filter, dim, metric string }{
		{"m4_phase", "eduide_session_phase_seconds", `phase=~"data_delivered|clone|ide_usable"`, "phase", "phase"},
		{"m6_lifetime", "eduide_session_lifetime_seconds", "", "reason", "lifetime"},
	} {
		b := c.q(ctx, h.id, fmt.Sprintf(`sum by (le, template, %s) (increase(%s_bucket%s[%s]))`, h.dim, h.base, c.sel(h.filter), d), at)
		for k, hist := range histograms(b, []string{"template", h.dim}) {
			r := newRow(w, h.metric, k[0], k[1], "seconds")
			r.Count, r.P50, r.P95, r.Max = hist.total(), hist.quantile(0.5), hist.quantile(0.95), hist.maxBucket()
			c.add(r)
		}
	}
	c.agentClaim(ctx, w, d, at)
	c.pool(ctx, w, d, at, hours)
	c.resources(ctx, w, d, at)
}

func (c *collector) add(r *Row) { r.suppress(); c.rows = append(c.rows, r) }

// launches is M1: launch counts and the largest counts inside an hour, a minute and 20 s and 60 s windows.
// shortcut: the 20 s and 60 s windows need at least two scrapes inside the window; otherwise they stay empty.
func (c *collector) launches(ctx context.Context, w window, d string, at time.Time, step string) {
	sets := map[string]string{"": "template", "all": ""}
	rows := map[string]*Row{}
	get := func(t string) *Row {
		if rows[t] == nil {
			rows[t] = newRow(w, "launches", t, "created", "launches")
		}
		return rows[t]
	}
	peak := func(id, win, sub string, set func(*Row, float64)) {
		for key, by := range sets {
			agg := "sum"
			if by != "" {
				agg = "sum by (" + by + ")"
			}
			v := c.q(ctx, id, fmt.Sprintf(`max_over_time(%s (increase(eduide_launch_total%s[%s]))[%s:%s])`, agg, c.sel(`result="created"`), win, d, sub), at)
			for _, s := range v {
				t := key
				if by != "" {
					t = label(s, []string{by}, 0)
				}
				set(get(t), math.Round(s.Value))
			}
		}
	}
	for key, by := range sets {
		agg := "sum"
		if by != "" {
			agg = "sum by (" + by + ")"
		}
		for _, s := range c.q(ctx, "m1_launches", fmt.Sprintf(`%s (increase(eduide_launch_total%s[%s]))`, agg, c.sel(`result="created"`), d), at) {
			t := key
			if by != "" {
				t = label(s, []string{by}, 0)
			}
			get(t).Count = math.Round(s.Value)
		}
	}
	peak("m1_peak_hour", "1h", "1h", func(r *Row, v float64) { r.PeakHour = v })
	peak("m1_peak_minute", "1m", "1m", func(r *Row, v float64) { r.PeakMinute = v })
	if c.resolves(20 * time.Second) {
		peak("m1_max_20s", "20s", step, func(r *Row, v float64) { r.Max20s = v })
	}
	if c.resolves(60 * time.Second) {
		peak("m1_max_60s", "60s", step, func(r *Row, v float64) { r.Max60s = v })
	}
	for _, r := range rows {
		if r.Count == 0 {
			continue // template without launches in this window
		}
		c.add(r)
	}
}

// resolves tells whether a window holds at least two scrapes.
func (c *collector) resolves(win time.Duration) bool { return c.scrape > 0 && win >= 2*c.scrape }

// startup is M2 and the lazy fallback share of M3.
func (c *collector) startup(ctx context.Context, w window, d string, at time.Time) {
	counts := c.q(ctx, "m2_startup_count", fmt.Sprintf(`sum by (template, start_mode, result) (increase(eduide_session_startup_seconds_count%s[%s]))`, c.sel(""), d), at)
	total, failed, perTemplate, fallback, hasMode := map[[2]string]float64{}, map[[2]string]float64{}, map[string]float64{}, map[string]float64{}, map[string]bool{}
	for _, s := range counts {
		k := [2]string{label(s, []string{"template"}, 0), label(s, []string{"start_mode"}, 0)}
		total[k] += s.Value
		perTemplate[k[0]] += s.Value
		if s.Labels["result"] == "failed" {
			failed[k] += s.Value
		}
		hasMode[k[0]] = hasMode[k[0]] || k[1] != "unknown"
		if k[1] == "lazy_fallback" {
			fallback[k[0]] += s.Value
		}
	}
	b := c.q(ctx, "m2_startup_bucket", fmt.Sprintf(`sum by (le, template, start_mode) (increase(eduide_session_startup_seconds_bucket%s[%s]))`, c.sel(`result="ready"`), d), at)
	hs := histograms(b, []string{"template", "start_mode"})
	for k, n := range total {
		r := newRow(w, "startup", k[0], k[1], "seconds")
		r.Count = math.Round(n)
		r.FailureShare = failed[k] / n
		if h := hs[k]; h != nil {
			r.P50, r.P95, r.Max = h.quantile(0.5), h.quantile(0.95), h.maxBucket()
		}
		c.add(r)
	}
	for t, n := range perTemplate {
		if !hasMode[t] {
			continue // the metric has no start_mode label yet
		}
		r := newRow(w, "lazy_fallback_share", t, "lazy_fallback", "share")
		r.Count, r.Value = math.Round(n), fallback[t]/n
		c.add(r)
	}
}

// running is the concurrency part of M1, per minute.
func (c *collector) running(ctx context.Context, w window, d string, at time.Time) {
	for key, by := range map[string]string{"": "template", "all": ""} {
		agg := "sum"
		if by != "" {
			agg = "sum by (" + by + ")"
		}
		inner := fmt.Sprintf(`%s (eduide_sessions%s)`, agg, c.sel(`phase="Ready"`))
		stats := map[string]*Row{}
		for _, st := range []struct {
			fn  string
			set func(*Row, float64)
		}{
			{"quantile_over_time(0.5, %s[%s:1m])", func(r *Row, v float64) { r.P50 = v }},
			{"quantile_over_time(0.95, %s[%s:1m])", func(r *Row, v float64) { r.P95 = v }},
			{"max_over_time(%s[%s:1m])", func(r *Row, v float64) { r.Max = v }},
		} {
			for _, s := range c.q(ctx, "m1_running", fmt.Sprintf(st.fn, inner, d), at) {
				t := key
				if by != "" {
					t = label(s, []string{by}, 0)
				}
				if stats[t] == nil {
					stats[t] = newRow(w, "running_sessions", t, "Ready", "sessions")
				}
				st.set(stats[t], math.Round(s.Value))
			}
		}
		for _, r := range stats {
			c.add(r)
		}
	}
}

func (c *collector) agentClaim(ctx context.Context, w window, d string, at time.Time) {
	all := c.q(ctx, "m4_agent_claim", fmt.Sprintf(`sum (increase(eduide_agent_requests_total%s[%s]))`, c.sel(`op="claim"`), d), at)
	bad := c.q(ctx, "m4_agent_claim_errors", fmt.Sprintf(`sum (increase(eduide_agent_requests_total%s[%s]))`, c.sel(`op="claim", result="error"`), d), at)
	if len(all) == 0 {
		return
	}
	r := newRow(w, "agent_claim", "all", "error", "requests")
	r.Count = math.Round(all[0].Value)
	if len(bad) > 0 && all[0].Value > 0 {
		r.FailureShare = bad[0].Value / all[0].Value
	} else if all[0].Value > 0 {
		r.FailureShare = 0
	}
	c.add(r)
}

// pool is M3: slot-hours by state and the hours without an unclaimed slot. The chart names a pool like its template.
func (c *collector) pool(ctx context.Context, w window, d string, at time.Time, hours float64) {
	for _, s := range c.q(ctx, "m3_pool_slot_hours", fmt.Sprintf(`sum by (pool, state) (avg_over_time(eduide_pool_instances%s[%s]))`, c.sel(""), d), at) {
		r := newRow(w, "pool_slot_hours", label(s, []string{"pool"}, 0), s.Labels["state"], "slot_hours")
		r.Value = s.Value * hours
		c.add(r)
	}
	for _, s := range c.q(ctx, "m3_pool_empty_hours", fmt.Sprintf(`sum_over_time((sum by (pool) (eduide_pool_instances%s) == bool 0)[%s:1m]) / 60`, c.sel(`state=~"idle_ready|idle_starting"`), d), at) {
		r := newRow(w, "pool_empty_hours", label(s, []string{"pool"}, 0), "no_unclaimed", "hours")
		r.Value = s.Value
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
	for _, m := range []struct{ metric, rec string }{{"restarts", "ide_restarts"}, {"oom_kills", "ide_oom_kills"}} {
		done := false
		for _, suffix := range []string{"_by_template_5m", "_5m"} {
			by := "template"
			if suffix == "_5m" {
				by = "namespace"
			}
			if done {
				break
			}
			for _, s := range c.q(ctx, "m7_"+m.metric+suffix, fmt.Sprintf(`sum by (%s) (sum_over_time(eduide:usage:%s%s%s[%s]))`, by, m.rec, suffix, c.sel(""), d), at) {
				t := "all"
				if by == "template" {
					t = label(s, []string{"template"}, 0)
				}
				r := newRow(w, m.metric, t, "ide_pods", m.metric)
				r.Value = math.Round(s.Value)
				c.add(r)
				done = true
			}
		}
	}
}

package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// src is the source of a counter: a plain selector, or an expression that is evaluated as a
// subquery (used to fold duplicate series of kube-state-metrics before differencing).
type src struct{ expr, sub string }

func plain(metric, sel string) src { return src{expr: metric + sel} }

func (s src) rng(w, offset string) string {
	r := s.expr + "[" + w + "]"
	if s.sub != "" {
		r = "(" + s.expr + ")[" + w + ":" + s.sub + "]"
	}
	if offset != "" {
		r += " offset " + offset
	}
	return r
}

// delta is the increase of a counter in the window w per series, without extrapolation: the last value
// in the window minus the value before it; a series that starts inside the window counts from zero,
// a series that dropped counts its new value. A reset inside the window loses the increments before it,
// which is why the export treats series with resets() > 0 in Go (counterTotals). The recording rules use
// this expression as it is.
func delta(metric, sel, w string) string { return deltaOf(plain(metric, sel), w) }

func deltaOf(m src, w string) string {
	look := "15m"
	if secs(w) >= 3600 {
		look = "1h"
	}
	l := "last_over_time(" + m.rng(w, "") + ")"
	p := "last_over_time(" + m.rng(look, w) + ")"
	return fmt.Sprintf("((%s - ((%s * 1) or (%s * 0))) >= 0) or (%s * 1)", l, p, l, l)
}

func secs(w string) int {
	n, _ := strconv.Atoi(w[:len(w)-1])
	return n
}

type step struct{ t, dt, d float64 }

// steps turns raw samples into increments. A sample at or before start only sets the baseline.
// A decrease is a counter reset: the new value counts in full. A series without a baseline counts from zero.
func steps(pts []point, start float64, prev *point) ([]step, *point) {
	var out []step
	for _, p := range pts {
		if prev != nil && p.t <= prev.t {
			continue
		}
		cp := p
		if p.t <= start {
			prev = &cp
			continue
		}
		st := step{t: p.t}
		if prev == nil {
			st.d = p.v
		} else {
			st.dt, st.d = p.t-prev.t, p.v-prev.v
			if st.d < 0 {
				st.d = p.v
			}
		}
		out = append(out, st)
		prev = &cp
	}
	return out, prev
}

func sumSteps(st []step) float64 {
	t := 0.0
	for _, s := range st {
		t += s.d
	}
	return t
}

func selector(metric string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}
	return metric + "{" + strings.Join(parts, ",") + "}"
}

func key(labels map[string]string, names []string) string {
	v := make([]string, len(names))
	for i, n := range names {
		v[i] = labels[n]
	}
	return strings.Join(v, "\x00")
}

// counterTotals returns the exact increase per group (labels groupBy) of a counter in window w.
// The bulk comes from one query; the few series with a reset inside the window are read as raw samples and
// summed in Go, so the increments before a reset are not lost. fixResets needs a plain selector.
func (c *collector) counterTotals(ctx context.Context, id string, metric string, m src, w window, groupBy []string, fixResets bool) []sample {
	d, at := w.dur(), w.end
	by := strings.Join(groupBy, ", ")
	agg := "sum"
	if by != "" {
		agg = "sum by (" + by + ")"
	}
	expr := fmt.Sprintf("%s (%s)", agg, deltaOf(m, d))
	if fixResets {
		expr = fmt.Sprintf("%s ((%s) unless (resets(%s) > 0))", agg, deltaOf(m, d), m.rng(d, ""))
	}
	totals := map[string]*sample{}
	var order []string
	add := func(labels map[string]string, v float64) {
		k := key(labels, groupBy)
		if totals[k] == nil {
			l := map[string]string{}
			for _, g := range groupBy {
				l[g] = labels[g]
			}
			totals[k] = &sample{Labels: l}
			order = append(order, k)
		}
		totals[k].Value += v
	}
	for _, s := range c.q(ctx, id, expr, at) {
		add(s.Labels, s.Value)
	}
	if fixResets {
		for _, r := range c.q(ctx, id+"_resets", fmt.Sprintf("resets(%s) > 0", m.rng(d, "")), at) {
			rs, err := c.p.queryRange(ctx, id+"_raw", fmt.Sprintf("%s[%ds]", selector(metric, r.Labels), secs(d)+3600), at)
			if err != nil && c.lastErr == nil {
				c.lastErr = err
			}
			for _, one := range rs {
				st, _ := steps(one.pts, float64(w.start.Unix()), nil)
				var inWindow []step
				for _, x := range st {
					if x.t <= float64(w.end.Unix()) {
						inWindow = append(inWindow, x)
					}
				}
				add(r.Labels, sumSteps(inWindow))
			}
		}
	}
	out := make([]sample, 0, len(order))
	for _, k := range order {
		out = append(out, *totals[k])
	}
	return out
}

// launchStats are the figures of M1 for one template or for the whole installation.
type launchStats struct {
	count, peakHour, peakMinute, max20, max60 float64
	gapSteps                                  int
}

// rawLaunches reads the launch counter as raw samples (in day chunks) and derives counts and peaks without
// extrapolation. A step across a scrape gap longer than the window is left out of that window's peak and counted
// in gapSteps; the totals still include it. shortcut: windows are cut at scrape samples, so a window spans its
// length up to its length plus one scrape interval.
func (c *collector) rawLaunches(ctx context.Context, w window) map[string]*launchStats {
	start, end := float64(w.start.Unix()), float64(w.end.Unix())
	byTemplate := map[string][]step{}
	prev := map[string]*point{}
	meta := map[string]map[string]string{}
	for from := w.start.Add(-15 * time.Minute); from.Before(w.end); from = from.Add(24 * time.Hour) {
		to := from.Add(24 * time.Hour)
		if to.After(w.end) {
			to = w.end
		}
		sel := c.sel(`result="created"`)
		rs, err := c.p.queryRange(ctx, "m1_launches_raw", fmt.Sprintf("eduide_launch_total%s[%ds]", sel, int(to.Sub(from).Seconds())), to)
		if err != nil && c.lastErr == nil {
			c.lastErr = err
		}
		for _, one := range rs {
			k := key(one.labels, []string{"pod", "instance", "template", "result"})
			meta[k] = one.labels
			st, p := steps(one.pts, start, prev[k])
			prev[k] = p
			t := label(sample{Labels: one.labels}, []string{"template"}, 0)
			for _, x := range st {
				if x.t <= end {
					byTemplate[t] = append(byTemplate[t], x)
				}
			}
		}
	}
	if len(byTemplate) == 0 {
		return nil
	}
	var all []step
	for _, st := range byTemplate {
		all = append(all, st...)
	}
	byTemplate["all"] = all
	out := map[string]*launchStats{}
	for t, st := range byTemplate {
		sort.Slice(st, func(i, j int) bool { return st[i].t < st[j].t })
		ls := &launchStats{count: sumSteps(st), max20: nan(), max60: nan()}
		ls.peakHour = bucketMax(st, 3600)
		ls.peakMinute = bucketMax(st, 60)
		if c.resolves(20 * time.Second) {
			ls.max20 = slidingMax(st, 20)
		}
		if c.resolves(60 * time.Second) {
			ls.max60 = slidingMax(st, 60)
		}
		for _, x := range st {
			if x.dt > 60 {
				ls.gapSteps++
			}
		}
		out[t] = ls
	}
	return out
}

// bucketMax is the largest sum in one clock-aligned bucket of the given length in seconds.
func bucketMax(st []step, length float64) float64 {
	sums := map[int64]float64{}
	for _, x := range st {
		if x.dt > length {
			continue
		}
		sums[int64(math.Floor((x.t-1e-9)/length))] += x.d
	}
	m := 0.0
	for _, v := range sums {
		m = math.Max(m, v)
	}
	return m
}

// slidingMax is the largest sum of increments in a window of width seconds that slides over the sample times.
func slidingMax(st []step, width float64) float64 {
	var in []step
	for _, x := range st {
		if x.dt <= width {
			in = append(in, x)
		}
	}
	m, sum, lo := 0.0, 0.0, 0
	for hi := range in {
		sum += in[hi].d
		for in[hi].t-in[lo].t >= width {
			sum -= in[lo].d
			lo++
		}
		m = math.Max(m, sum)
	}
	return m
}

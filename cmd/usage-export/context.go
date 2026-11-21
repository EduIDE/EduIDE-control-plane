package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

type poolSize struct {
	Template     string   `json:"template"`
	MinIdle      *float64 `json:"min_idle,omitempty"`
	MaxInstances *float64 `json:"max_instances,omitempty"`
}

type templateResource struct {
	Template string  `json:"template"`
	Resource string  `json:"resource"`
	Kind     string  `json:"kind"`
	Value    float64 `json:"value"`
}

type timeout struct {
	Template string  `json:"template"`
	Kind     string  `json:"kind"`
	Seconds  float64 `json:"seconds"`
}

type dataGap struct {
	Source             string `json:"source"`
	Date               string `json:"date"`
	MinutesWithoutData int    `json:"minutes_without_data"`
	MinutesTargetDown  int    `json:"minutes_target_down"`
}

type contextFile struct {
	SchemaVersion int    `json:"schema_version"`
	Installation  string `json:"installation"`
	Release       string `json:"release"`
	ChartVersion  string `json:"chart_version"`
	Period        struct {
		From          string   `json:"from"`
		To            string   `json:"to"`
		RequestedFrom string   `json:"requested_from"`
		RequestedTo   string   `json:"requested_to"`
		Note          string   `json:"note"`
		DroppedWeeks  []string `json:"dropped_incomplete_weeks"`
	} `json:"period"`
	ScrapeIntervalSeconds *float64 `json:"scrape_interval_seconds"`
	ScrapeIntervalMin     *float64 `json:"scrape_interval_min_seconds"`
	ScrapeIntervalMax     *float64 `json:"scrape_interval_max_seconds"`
	LaunchWindows         struct {
		StepSeconds      float64 `json:"step_seconds"`
		Window20Resolved bool    `json:"window_20s_resolved"`
		Window60Resolved bool    `json:"window_60s_resolved"`
		Method           string  `json:"method"`
	} `json:"launch_windows"`
	Timeouts          []timeout          `json:"template_timeouts"`
	PoolSizes         []poolSize         `json:"pool_sizes"`
	TemplateResources []templateResource `json:"template_resources"`
	Nodes             struct {
		Count                  *float64 `json:"count"`
		AllocatableCPUCores    *float64 `json:"allocatable_cpu_cores"`
		AllocatableMemoryBytes *float64 `json:"allocatable_memory_bytes"`
		Scope                  string   `json:"scope"`
	} `json:"nodes"`
	DataGaps      []dataGap           `json:"data_gaps"`
	MissingSeries []string            `json:"missing_series"`
	MissingByWeek map[string][]string `json:"missing_series_by_week"`
	NotCollected  []string            `json:"not_collected"`
}

func first(v []sample) *float64 {
	if len(v) == 0 {
		return nil
	}
	return &v[0].Value
}

// scrapeInterval infers the interval from the number of samples `up` has in ten minutes.
func (c *collector) scrapeInterval(ctx context.Context, from, at time.Time) (med, lo, hi *float64) {
	// The interval of the best-sampled control plane target every hour of the period; the median decides the resolution.
	inner := fmt.Sprintf(`(600 / max(count_over_time(up%s[10m])))`, c.controlPlane())
	span := int(at.Sub(from).Seconds())
	get := func(id, fn string) *float64 {
		v := first(c.q(ctx, id, fmt.Sprintf(fn, inner, span), at))
		if v == nil {
			return nil
		}
		r := math.Round(*v)
		return &r
	}
	med = get("ctx_scrape_interval", "quantile_over_time(0.5, %s[%ds:1h])")
	lo = get("ctx_scrape_interval_min", "min_over_time(%s[%ds:1h])")
	hi = get("ctx_scrape_interval_max", "max_over_time(%s[%ds:1h])")
	if med != nil {
		c.scrape = time.Duration(*med) * time.Second
	}
	return
}

func (c *collector) context(ctx context.Context, install, release, chart string, from, to time.Time, end time.Time) contextFile {
	cf := contextFile{Timeouts: []timeout{}, PoolSizes: []poolSize{}, TemplateResources: []templateResource{}}
	at := end
	cf.SchemaVersion, cf.Installation, cf.Release, cf.ChartVersion = schemaVersion, install, release, chart
	cf.Period.From, cf.Period.To = from.Format("2006-01-02"), to.Format("2006-01-02")
	cf.Period.DroppedWeeks = []string{}
	cf.ScrapeIntervalSeconds, cf.ScrapeIntervalMin, cf.ScrapeIntervalMax = c.scrapeInterval(ctx, from, at)
	cf.LaunchWindows.StepSeconds = c.scrape.Seconds()
	cf.LaunchWindows.Window20Resolved, cf.LaunchWindows.Window60Resolved = c.resolves(20*time.Second), c.resolves(60*time.Second)
	cf.LaunchWindows.Method = "recording rules eduide:usage:launches_max_20s_in_minute and eduide:usage:launches_max_60s_in_minute: exact counter deltas of eduide_launch_total{result=created} over 20 s or 60 s, evaluated every 10 s, maximum per minute, then maximum over the week; a window needs at least two scrapes, otherwise the column stays empty"

	pools := map[string]*poolSize{}
	for _, m := range []struct{ id, name string }{{"ctx_pool_min_idle", "eduide_pool_min_idle"}, {"ctx_pool_max_instances", "eduide_pool_max_instances"}} {
		for _, s := range c.q(ctx, m.id, fmt.Sprintf(`max by (template) (%s%s)`, m.name, c.sel("")), at) {
			t := label(s, []string{"template"}, 0)
			if pools[t] == nil {
				pools[t] = &poolSize{Template: t}
			}
			v := s.Value
			if m.name == "eduide_pool_min_idle" {
				pools[t].MinIdle = &v
			} else {
				pools[t].MaxInstances = &v
			}
		}
	}
	for _, p := range pools {
		cf.PoolSizes = append(cf.PoolSizes, *p)
	}
	sort.Slice(cf.PoolSizes, func(i, j int) bool { return cf.PoolSizes[i].Template < cf.PoolSizes[j].Template })
	for _, s := range c.q(ctx, "ctx_template_resources", fmt.Sprintf(`max by (template, resource, kind) (eduide_template_resources%s)`, c.sel("")), at) {
		cf.TemplateResources = append(cf.TemplateResources, templateResource{label(s, []string{"template"}, 0), s.Labels["resource"], s.Labels["kind"], s.Value})
	}
	sort.Slice(cf.TemplateResources, func(i, j int) bool {
		a, b := cf.TemplateResources[i], cf.TemplateResources[j]
		return a.Template+a.Resource+a.Kind < b.Template+b.Resource+b.Kind
	})

	for _, s := range c.q(ctx, "ctx_template_timeouts", fmt.Sprintf(`max by (template, kind) (eduide_template_timeout_seconds%s)`, c.sel("")), at) {
		cf.Timeouts = append(cf.Timeouts, timeout{label(s, []string{"template"}, 0), s.Labels["kind"], s.Value})
	}
	sort.Slice(cf.Timeouts, func(i, j int) bool {
		return cf.Timeouts[i].Template+cf.Timeouts[i].Kind < cf.Timeouts[j].Template+cf.Timeouts[j].Kind
	})

	cf.Nodes.Scope = "all nodes known to kube-state-metrics, not only the nodes that run session pods"
	cf.Nodes.Count = first(c.q(ctx, "ctx_nodes", `count(kube_node_info)`, at))
	cf.Nodes.AllocatableCPUCores = first(c.q(ctx, "ctx_node_cpu", `sum(kube_node_status_allocatable{resource="cpu"})`, at))
	cf.Nodes.AllocatableMemoryBytes = first(c.q(ctx, "ctx_node_memory", `sum(kube_node_status_allocatable{resource="memory"})`, at))

	cf.DataGaps = c.gaps(ctx, from, end)
	cf.MissingSeries = c.p.missing()
	cf.NotCollected = []string{"peak-hour aggregates of M5 (memory and CPU are weekly only)"}
	return cf
}

// gaps reports the days with five or more minutes without any scraped target, or with a target that is down,
// for the control plane targets (M1 to M4, M6) and for kube-state-metrics (M5, M7). The first bin is the hour
// before the period: the look-back of the delta queries reaches into it.
func (c *collector) gaps(ctx context.Context, from, end time.Time) []dataGap {
	out := []dataGap{}
	sources := []struct{ name, sel string }{
		{"control-plane", c.controlPlane()},
		{"kube-state-metrics", `{job="kube-state-metrics"}`},
	}
	type bin struct {
		label      string
		start, end time.Time
	}
	bins := []bin{{"before-period", from.Add(-time.Hour), from}}
	for day := from; day.Before(end); day = day.AddDate(0, 0, 1) {
		next := day.AddDate(0, 0, 1)
		if next.After(end) {
			next = end
		}
		bins = append(bins, bin{day.Format("2006-01-02"), day, next})
	}
	for _, src := range sources {
		for _, b := range bins {
			span := int(b.end.Sub(b.start).Seconds())
			mins := span / 60
			up := first(c.q(ctx, "ctx_up_samples", fmt.Sprintf(`count_over_time(min(up%s)[%ds:1m])`, src.sel, span), b.end))
			down := first(c.q(ctx, "ctx_up_down", fmt.Sprintf(`count_over_time((min(up%s) == 0)[%ds:1m])`, src.sel, span), b.end))
			g := dataGap{Source: src.name, Date: b.label, MinutesWithoutData: mins}
			if up != nil {
				g.MinutesWithoutData = int(math.Max(0, float64(mins)-*up))
			}
			if down != nil {
				g.MinutesTargetDown = int(*down)
			}
			if g.MinutesWithoutData >= 5 || g.MinutesTargetDown >= 5 {
				out = append(out, g)
			}
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644) //nolint:gosec // the export is meant to be shared
}

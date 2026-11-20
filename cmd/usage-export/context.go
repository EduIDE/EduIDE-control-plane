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

type dataGap struct {
	Date               string `json:"date"`
	MinutesWithoutData int    `json:"minutes_without_data"`
	MinutesTargetDown  int    `json:"minutes_target_down"`
}

type contextFile struct {
	SchemaVersion int    `json:"schema_version"`
	Installation  string `json:"installation"`
	Namespace     string `json:"namespace"`
	Period        struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"period"`
	ScrapeIntervalSeconds *float64 `json:"scrape_interval_seconds"`
	LaunchWindows         struct {
		StepSeconds      float64 `json:"step_seconds"`
		Window20Resolved bool    `json:"window_20s_resolved"`
		Window60Resolved bool    `json:"window_60s_resolved"`
		Method           string  `json:"method"`
	} `json:"launch_windows"`
	PoolSizes         []poolSize         `json:"pool_sizes"`
	TemplateResources []templateResource `json:"template_resources"`
	Nodes             struct {
		Count                  *float64 `json:"count"`
		AllocatableCPUCores    *float64 `json:"allocatable_cpu_cores"`
		AllocatableMemoryBytes *float64 `json:"allocatable_memory_bytes"`
		Scope                  string   `json:"scope"`
	} `json:"nodes"`
	DataGaps      []dataGap `json:"data_gaps"`
	MissingSeries []string  `json:"missing_series"`
	NotCollected  []string  `json:"not_collected"`
}

func first(v []sample) *float64 {
	if len(v) == 0 {
		return nil
	}
	return &v[0].Value
}

// scrapeInterval infers the interval from the number of samples `up` has in ten minutes.
func (c *collector) scrapeInterval(ctx context.Context, at time.Time) *float64 {
	v := first(c.q(ctx, "ctx_scrape_interval", fmt.Sprintf(`600 / max(count_over_time(up%s[10m]))`, c.sel("")), at))
	if v == nil {
		return nil
	}
	r := math.Round(*v)
	c.scrape = time.Duration(r) * time.Second
	return &r
}

func (c *collector) context(ctx context.Context, install string, from, to time.Time, end time.Time) contextFile {
	cf := contextFile{PoolSizes: []poolSize{}, TemplateResources: []templateResource{}}
	at := end
	cf.SchemaVersion, cf.Installation, cf.Namespace = schemaVersion, install, c.ns
	cf.Period.From, cf.Period.To = from.Format("2006-01-02"), to.Format("2006-01-02")
	cf.ScrapeIntervalSeconds = c.scrapeInterval(ctx, at)
	cf.LaunchWindows.StepSeconds = c.scrape.Seconds()
	cf.LaunchWindows.Window20Resolved, cf.LaunchWindows.Window60Resolved = c.resolves(20*time.Second), c.resolves(60*time.Second)
	cf.LaunchWindows.Method = "max_over_time of increase(eduide_launch_total{result=created}[window]) evaluated every scrape interval; a window needs at least two scrapes, otherwise the column stays empty"

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

	cf.Nodes.Scope = "all nodes known to kube-state-metrics, not only the nodes that run session pods"
	cf.Nodes.Count = first(c.q(ctx, "ctx_nodes", `count(kube_node_info)`, at))
	cf.Nodes.AllocatableCPUCores = first(c.q(ctx, "ctx_node_cpu", `sum(kube_node_status_allocatable{resource="cpu"})`, at))
	cf.Nodes.AllocatableMemoryBytes = first(c.q(ctx, "ctx_node_memory", `sum(kube_node_status_allocatable{resource="memory"})`, at))

	cf.DataGaps = c.gaps(ctx, from, end)
	cf.MissingSeries = c.p.missing()
	cf.NotCollected = []string{"EduIDE release and chart version", "session timeout", "peak-hour aggregates of M5 (memory and CPU are weekly only)", "claims on a not-ready instance"}
	return cf
}

// gaps reports the days with five or more minutes without any scraped target of the namespace, or with a target that is down.
func (c *collector) gaps(ctx context.Context, from, end time.Time) []dataGap {
	out := []dataGap{}
	for day := from; day.Before(end); day = day.AddDate(0, 0, 1) {
		next := day.AddDate(0, 0, 1)
		if next.After(end) {
			next = end
		}
		mins := int(next.Sub(day).Minutes())
		up := first(c.q(ctx, "ctx_up_samples", fmt.Sprintf(`count_over_time(min(up%s)[%ds:1m])`, c.sel(""), int(next.Sub(day).Seconds())), next))
		down := first(c.q(ctx, "ctx_up_down", fmt.Sprintf(`count_over_time((min(up%s) == 0)[%ds:1m])`, c.sel(""), int(next.Sub(day).Seconds())), next))
		g := dataGap{Date: day.Format("2006-01-02"), MinutesWithoutData: mins}
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
	return out
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644) //nolint:gosec // the export is meant to be shared
}

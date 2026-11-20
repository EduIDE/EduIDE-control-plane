package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWindow() window {
	return window{time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
}

func testCollector(t *testing.T, fn func(q string) string, scrape time.Duration) *collector {
	t.Helper()
	srv, _ := fakePromFn(t, fn)
	p, err := newProm(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &collector{p: p, ns: "eduide-test", scrape: scrape}
}

func TestStepsHandleResetsAndNewSeries(t *testing.T) {
	// 5 before the window, +4, reset, +2: the true increase is 6, a last-minus-first delta would give 2.
	st, _ := steps([]point{{0, 5}, {100, 9}, {130, 2}}, 50, nil)
	if got := sumSteps(st); got != 6 {
		t.Fatalf("reset: %v", got)
	}
	// A series that starts inside the window counts from zero, including its first increment.
	st, _ = steps([]point{{100, 1}, {130, 3}}, 50, nil)
	if got := sumSteps(st); got != 3 {
		t.Fatalf("new series: %v", got)
	}
}

func TestCounterTotalsReadResetSeriesAsRawSamples(t *testing.T) {
	w := testWindow()
	c := testCollector(t, func(q string) string {
		switch {
		case strings.HasPrefix(q, "resets("):
			return vec(s("1", `"template":"java","pod":"svc-a"`))
		case strings.Contains(q, "unless (resets("):
			// The bulk query excludes the reset series: only the healthy series is left.
			return vec(s("10", `"template":"java"`))
		case strings.Contains(q, `pod="svc-a"`):
			return matrixResult(matrix(`"template":"java","pod":"svc-a"`, [2]float64{-60, 5}, [2]float64{100, 9}, [2]float64{130, 2}))
		}
		return ""
	}, 10*time.Second)
	got := c.counterTotals(context.Background(), "x", "eduide_launch_total", plain("eduide_launch_total", c.sel("")), w, []string{"template"}, true)
	if len(got) != 1 || got[0].Labels["template"] != "java" || got[0].Value != 16 {
		t.Fatalf("totals: %+v (want 10 from the bulk and 6 from the reset series)", got)
	}
}

func TestRawLaunchesDoNotAttributeAGapStepToOneWindow(t *testing.T) {
	// Counter 100, a gap of 20 minutes, then 101: the single launch is counted in the total but is not a peak minute.
	c := testCollector(t, func(q string) string {
		return matrixResult(
			matrix(`"template":"java","pod":"svc-a"`, [2]float64{-10, 100}, [2]float64{0, 100}, [2]float64{10, 100}, [2]float64{1210, 101}),
			matrix(`"template":"java","pod":"svc-b"`, [2]float64{-10, 7}, [2]float64{0, 7}, [2]float64{10, 8}, [2]float64{20, 8}, [2]float64{30, 8}, [2]float64{40, 9}, [2]float64{50, 9}))
	}, 10*time.Second)
	ls := c.rawLaunches(context.Background(), testWindow())["java"]
	if ls == nil || ls.count != 3 {
		t.Fatalf("stats: %+v", ls)
	}
	if ls.peakMinute != 2 || ls.max60 != 2 || ls.max20 != 1 {
		t.Fatalf("peaks must come from steady scrapes only: %+v", ls)
	}
	if ls.gapSteps != 1 {
		t.Fatalf("gap step not marked: %+v", ls)
	}
}

func TestRawLaunchesWithoutSeries(t *testing.T) {
	c := testCollector(t, func(string) string { return "" }, 10*time.Second)
	if got := c.rawLaunches(context.Background(), testWindow()); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestRestartsFoldKubeStateMetricsInstancesAndSplitByTemplate(t *testing.T) {
	c := testCollector(t, func(q string) string {
		switch {
		case strings.Contains(q, "kube_pod_container_status_restarts_total"):
			return vec(s("2", `"namespace":"eduide-test","pod":"ide-aaaaaaaaaaaaaaaaaaaa","container":"ide"`), s("1", `"namespace":"eduide-test","pod":"ide-bbbbbbbbbbbbbbbbbbbb","container":"ide"`))
		case strings.Contains(q, "last_terminated_reason"):
			return vec(s("1", `"pod":"ide-aaaaaaaaaaaaaaaaaaaa","container":"ide"`))
		case strings.Contains(q, "kube_pod_labels"):
			return vec(s("1", `"pod":"ide-aaaaaaaaaaaaaaaaaaaa","label_eduide_github_io_template":"java"`), s("1", `"pod":"ide-bbbbbbbbbbbbbbbbbbbb","label_eduide_github_io_template":"py"`))
		}
		return ""
	}, 10*time.Second)
	c.restarts(context.Background(), testWindow(), testWindow().dur(), testWindow().end)
	var folded bool
	for _, q := range queries {
		if strings.Contains(q, "kube_pod_container_status_restarts_total") {
			folded = strings.Contains(q, "max by (namespace, pod, container) (kube_pod_container_status_restarts_total")
		}
	}
	if !folded {
		t.Fatalf("restart series are not folded by (namespace, pod, container): %v", queries)
	}
	got := map[string]float64{}
	for _, r := range c.rows {
		got[r.Metric+"/"+r.Template] = r.Value
	}
	if got["restarts/java"] != 2 || got["restarts/py"] != 1 || got["oom_kills/java"] != 2 || got["oom_kills/py"] != 0 {
		t.Fatalf("rows: %v", got)
	}
}

// TestDeltaFoldsDuplicateSeries checks, with promtool, that a counter that reappears under a new instance label
// (kube-state-metrics restarted) is not counted again once the series are folded with max by.
func TestDeltaFoldsDuplicateSeries(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not installed")
	}
	dir := t.TempDir()
	rules := "groups:\n- name: t\n  rules:\n  - record: test:folded\n    expr: sum (" + deltaOf(src{expr: "max by (pod) (c)", sub: "30s"}, "300s") + ")\n"
	tests := `rule_files: [rules.yml]
evaluation_interval: 30s
tests:
- interval: 30s
  input_series:
  # the first kube-state-metrics instance until t=300 s, the second one from t=330 s; the counter goes 5 to 6
  - series: 'c{pod="p", instance="a"}'
    values: '5x10 _x10'
  - series: 'c{pod="p", instance="b"}'
    values: '_x11 5 5 5 6 6 6 6 6 6'
  promql_expr_test:
  - expr: test:folded
    eval_time: 10m
    exp_samples:
    - labels: 'test:folded'
      value: 1
`
	for name, body := range map[string]string{"rules.yml": rules, "test.yml": tests} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(bin, "test", "rules", filepath.Join(dir, "test.yml")).CombinedOutput(); err != nil { //nolint:gosec // fixed binary and a temp file
		t.Fatalf("promtool: %v\n%s", err, out)
	}
}

func TestClaimedPoolHoursAreSuppressedForSmallTemplates(t *testing.T) {
	canned := func(launches float64) map[string]string {
		return map[string]string{
			"eduide_launch_total": matrixResult(matrix(`"template":"java","pod":"svc-a"`, [2]float64{-30, 0}, [2]float64{60, launches})),
			"sum_over_time(max by (pool, state)": vec(
				s("30", `"pool":"java","state":"claimed"`),
				s("100", `"pool":"java","state":"idle_ready"`)),
			"== bool 0": vec(s("2.5", `"pool":"java"`)),
		}
	}
	small, _ := runExport(t, canned(4))
	rows := readRows(t, small)
	for _, dim := range [][2]string{{"pool_slot_hours", "claimed"}, {"pool_empty_hours", "no_unclaimed"}} {
		r := find(rows, dim[0], "java", dim[1])
		if r == nil || r["suppressed"] != "true" || r["value"] != "" || r["count"] != "4" {
			t.Fatalf("%v with 4 sessions must be suppressed: %v", dim, r)
		}
	}
	if r := find(rows, "pool_slot_hours", "java", "idle_ready"); r == nil || r["suppressed"] != "false" || r["value"] != "100" {
		t.Fatalf("idle hours do not depend on sessions: %v", r)
	}
	large, _ := runExport(t, canned(40))
	if r := find(readRows(t, large), "pool_slot_hours", "java", "claimed"); r == nil || r["suppressed"] != "false" || r["value"] != "30" {
		t.Fatalf("40 sessions: %v", r)
	}
	if found, err := checkDir(small); err != nil || len(found) != 0 {
		t.Fatalf("own export fails the check: %v %v", found, err)
	}
}

func TestGapSourcesCoverKubeStateMetrics(t *testing.T) {
	c := testCollector(t, func(q string) string {
		if strings.Contains(q, `job="kube-state-metrics"`) && strings.Contains(q, "3600s:1m") && !strings.Contains(q, "== 0") {
			return vec(s("0", ""))
		}
		return ""
	}, 10*time.Second)
	from := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	gaps := c.gaps(context.Background(), from, from.AddDate(0, 0, 1))
	var pre bool
	for _, g := range gaps {
		if g.Source == "kube-state-metrics" && g.Date == "before-period" && g.MinutesWithoutData == 60 {
			pre = true
		}
	}
	if !pre {
		t.Fatalf("gap in the hour before the period of kube-state-metrics not found: %+v", gaps)
	}
}

package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func vec(rows ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`
}

func s(v string, labels string) string {
	return fmt.Sprintf(`{"metric":{%s},"value":[1,%q]}`, labels, v)
}

// queries holds every expression the fake saw; tests reset it.
var queries []string

// fakeProm answers a query with the first canned response whose key is a substring of the expression;
// everything else is an empty vector. It records the paths it was asked for.
func fakeProm(t *testing.T, canned map[string]string) (*httptest.Server, *[]string) {
	return fakePromFn(t, func(q string) string {
		for k, v := range canned {
			if strings.Contains(q, k) {
				return v
			}
		}
		return ""
	})
}

// fakePromFn answers with whatever fn returns for the expression; "" is an empty vector.
func fakePromFn(t *testing.T, fn func(q string) string) (*httptest.Server, *[]string) {
	t.Helper()
	queries = nil
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		if v := fn(q); v != "" {
			_, _ = w.Write([]byte(v))
			return
		}
		_, _ = w.Write([]byte(vec()))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func bucket(tpl, mode, le, v string) string {
	return s(v, fmt.Sprintf(`"template":%q,"start_mode":%q,"le":%q`, tpl, mode, le))
}

func runExport(t *testing.T, canned map[string]string) (string, *[]string) {
	t.Helper()
	srv, paths := fakeProm(t, canned)
	p, err := newProm(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	from := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if err := export(context.Background(), p, "eduide-test", "test", "3.0.0", "0.2.1", from, to, [2]time.Time{from, to}, false, to.AddDate(0, 0, 3), dir); err != nil {
		t.Fatal(err)
	}
	return dir, paths
}

func readRows(t *testing.T, dir string) []map[string]string {
	t.Helper()
	fh, err := os.Open(filepath.Join(dir, "weekly.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	recs, err := csv.NewReader(fh).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, r := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func find(rows []map[string]string, metric, tpl, dim string) map[string]string {
	for _, r := range rows {
		if r["metric"] == metric && r["template"] == tpl && r["dimension"] == dim {
			return r
		}
	}
	return nil
}

func TestExportEndToEnd(t *testing.T) {
	canned := map[string]string{
		"ctx_never": "",
		"600 / max": vec(s("10", "")),
		"startup_count_5m": vec(
			s("30", `"template":"java","start_mode":"eager","result":"ready","strategy":"warm-ready"`),
			s("10", `"template":"java","start_mode":"eager","result":"ready","strategy":"warm-starting"`),
			s("2", `"template":"java","start_mode":"eager","result":"failed"`),
			s("8", `"template":"java","start_mode":"lazy_fallback","result":"ready"`),
			s("3", `"template":"py","start_mode":"lazy","result":"ready"`)),
		"startup_bucket_5m": vec(
			bucket("java", "eager", "1", "10"), bucket("java", "eager", "2", "30"), bucket("java", "eager", "+Inf", "40"),
			bucket("java", "lazy_fallback", "10", "8"), bucket("java", "lazy_fallback", "+Inf", "8"),
			bucket("py", "lazy", "10", "3"), bucket("py", "lazy", "+Inf", "3")),
		"launches_per_minute": vec(s("50", `"template":"java"`)),
		"pool_instances":      vec(s("120", `"template":"java","state":"idle_ready"`)),
		"ide_restarts_5m":     vec(s("3", `"template":"java"`)),
		"ide_oom_kills_5m":    vec(s("1", `"template":"java"`)),
	}
	dir, paths := runExport(t, canned)
	for _, p := range *paths {
		if p != "GET /api/v1/query" {
			t.Fatalf("unexpected request %s", p)
		}
	}
	rows := readRows(t, dir)

	e := find(rows, "startup", "java", "eager")
	if e == nil || e["count"] != "42" || e["suppressed"] != "false" || e["p50"] == "" || e["max"] != "+Inf" {
		t.Fatalf("java eager row: %v", e)
	}
	if e["failure_share"] != "0.047619" {
		t.Fatalf("failure share: %v", e["failure_share"])
	}
	ns := find(rows, "not_ready_claim_share", "java", "warm-starting")
	if ns == nil || ns["value"] != "0.25" || ns["count"] != "40" {
		t.Fatalf("not-ready claim share: %v", ns)
	}
	if l := find(rows, "launches", "java", "created"); l == nil || l["count"] != "50" || l["peak_hour"] != "50" {
		t.Fatalf("launches: %v", l)
	}
	if l := find(rows, "launches", "all", "created"); l == nil || l["count"] != "50" {
		t.Fatalf("installation-wide launches: %v", l)
	}
	if p := find(rows, "pool_slot_hours", "java", "idle_ready"); p == nil || p["value"] != "120" {
		t.Fatalf("pool slot hours: %v", p)
	}
	if r, o := find(rows, "restarts", "java", "ide_pods"), find(rows, "oom_kills", "java", "ide_pods"); r == nil || r["value"] != "3" || o == nil || o["value"] != "1" {
		t.Fatalf("restarts %v, oom kills %v", r, o)
	}
	// 8 and 3 sessions are below the small-cell limit: count only.
	for _, k := range [][2]string{{"java", "lazy_fallback"}, {"py", "lazy"}} {
		r := find(rows, "startup", k[0], k[1])
		if r == nil || r["suppressed"] != "true" || r["p50"] != "" || r["p95"] != "" || r["max"] != "" || r["failure_share"] != "" || r["count"] == "" {
			t.Fatalf("cell %v not suppressed: %v", k, r)
		}
	}
	// The 20 s window needs a scrape interval of at most 10 s; with 10 s it resolves.
	cf := map[string]any{}
	b, _ := os.ReadFile(filepath.Join(dir, "context.json"))
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	lw := cf["launch_windows"].(map[string]any)
	if cf["scrape_interval_seconds"].(float64) != 10 || lw["window_20s_resolved"] != true {
		t.Fatalf("context: %v", cf)
	}
	if len(cf["missing_series"].([]any)) == 0 {
		t.Fatal("missing series not listed")
	}
	if cf["release"] != "3.0.0" || cf["chart_version"] != "0.2.1" {
		t.Fatalf("release not in context: %v", cf)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	if found, err := checkDir(dir); err != nil || len(found) != 0 {
		t.Fatalf("export fails its own check: %v %v", found, err)
	}
}

func TestSlowScrapeLeavesShortWindowsEmpty(t *testing.T) {
	dir, _ := runExport(t, map[string]string{
		"600 / max":           vec(s("30", "")),
		"launches_per_minute": vec(s("50", `"template":"java"`)),
		"launches_max_":       vec(s("12", `"template":"java"`)),
	})
	r := find(readRows(t, dir), "launches", "java", "created")
	if r == nil || r["max_20s"] != "" || r["max_60s"] == "" {
		t.Fatalf("with 30 s scrapes only the 60 s window resolves: %v", r)
	}
}

func TestMissingSeriesGiveEmptyFiles(t *testing.T) {
	dir, _ := runExport(t, nil)
	if rows := readRows(t, dir); len(rows) != 0 {
		t.Fatalf("rows without data: %v", rows)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "context.json"))
	if !strings.Contains(string(b), `"missing_series"`) || !strings.Contains(string(b), "m2_startup_count") {
		t.Fatalf("missing series not recorded: %s", b)
	}
}

func TestSuppress(t *testing.T) {
	w := window{time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	for _, tc := range []struct {
		count float64
		want  bool
	}{{9, true}, {10, false}, {0, true}, {math.NaN(), true}} {
		r := newRow(w, "startup", "t", "eager", "seconds")
		r.Count, r.P50, r.Max20s = tc.count, 1, 2
		r.suppress()
		if r.Suppressed != tc.want || (tc.want && (!math.IsNaN(r.P50) || !math.IsNaN(r.Max20s))) || (!tc.want && math.IsNaN(r.P50)) {
			t.Errorf("count %v: %+v", tc.count, r)
		}
	}
}

func TestWeeks(t *testing.T) {
	// Wednesday 2026-11-11 to Tuesday 2026-11-17 crosses one Monday.
	ws := weeks(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	if len(ws) != 2 || ws[0].label() != "2026-W46" || ws[0].days() != 5 || ws[1].label() != "2026-W47" || ws[1].days() != 2 {
		t.Fatalf("%+v", ws)
	}
}

func TestQuantile(t *testing.T) {
	h := histograms([]sample{
		{map[string]string{"le": "+Inf"}, 10}, {map[string]string{"le": "1"}, 4}, {map[string]string{"le": "2"}, 8},
	}, []string{"x"})[[2]string{"unknown", ""}]
	if got := h.quantile(0.5); got != 1.25 {
		t.Errorf("p50 = %v", got)
	}
	if got := h.quantile(0.95); got != 2 {
		t.Errorf("p95 in +Inf bucket = %v", got)
	}
	if got := h.maxBucket(); !math.IsInf(got, 1) {
		t.Errorf("max = %v", got)
	}
}

func TestWriteCSV(t *testing.T) {
	w := window{time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	a := newRow(w, "startup", "b", "lazy", "seconds")
	a.Count, a.P50 = 12, 1.5
	b := newRow(w, "startup", "a", "eager", "seconds")
	b.Count = 3
	b.suppress()
	path := filepath.Join(t.TempDir(), "weekly.csv")
	if err := writeCSV(path, []*Row{a, b}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	want := []string{
		strings.Join(csvHeader, ","),
		"2026-W46,2026-11-09,7,startup,a,eager,seconds,3,true,,,,,,,,,",
		"2026-W46,2026-11-09,7,startup,b,lazy,seconds,12,false,,1.5,,,,,,,",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestPromRejectsBadURL(t *testing.T) {
	if _, err := newProm("file:///etc/passwd"); err == nil {
		t.Fatal("accepted a file URL")
	}
}

func TestReleaseAndChartVersionAreRequired(t *testing.T) {
	err := run([]string{"--prometheus", "http://127.0.0.1:1", "--namespace", "x", "--installation", "x", "--out", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "--release") {
		t.Fatalf("err = %v", err)
	}
}

func TestTemplateTimeoutsInContext(t *testing.T) {
	dir, _ := runExport(t, map[string]string{"eduide_template_timeout_seconds": vec(s("1800", `"template":"java","kind":"idle"`))})
	b, _ := os.ReadFile(filepath.Join(dir, "context.json"))
	if !strings.Contains(string(b), `"kind": "idle"`) {
		t.Fatalf("timeouts missing: %s", b)
	}
}

func TestSuppressMissingCountAndExemptRows(t *testing.T) {
	w := window{time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	r := newRow(w, "running_sessions", "t", "Ready", "sessions")
	r.P95 = 4
	r.suppress()
	if !r.Suppressed || !math.IsNaN(r.P95) {
		t.Fatalf("a row without a count must be suppressed: %+v", r)
	}
	p := newRow(w, "pool_slot_hours", "t", "idle_ready", "slot_hours")
	p.Value, p.noCell = 12, true
	p.suppress()
	if p.Suppressed || p.Value != 12 {
		t.Fatalf("pool rows are not per-session figures: %+v", p)
	}
}

func TestStartupSuppressesOnTheCountOfTheDistribution(t *testing.T) {
	// 5 ready and 20 failed: 25 starts, but the quantiles would describe 5 sessions.
	dir, _ := runExport(t, map[string]string{
		"startup_count_5m": vec(
			s("5", `"template":"java","start_mode":"eager","result":"ready"`),
			s("20", `"template":"java","start_mode":"eager","result":"failed"`)),
		"startup_bucket_5m": vec(bucket("java", "eager", "1", "5"), bucket("java", "eager", "+Inf", "5")),
	})
	r := find(readRows(t, dir), "startup", "java", "eager")
	if r == nil || r["count"] != "25" || r["suppressed"] != "true" || r["p50"] != "" || r["p95"] != "" || r["max"] != "" || r["failure_share"] != "0.8" {
		t.Fatalf("row: %v", r)
	}
}

func TestExactCountsAreNotRounded(t *testing.T) {
	dir, _ := runExport(t, map[string]string{"startup_count_5m": vec(s("9.4", `"template":"java","start_mode":"eager","result":"ready"`))})
	r := find(readRows(t, dir), "startup", "java", "eager")
	if r == nil || r["count"] != "9.4" || r["suppressed"] != "true" {
		t.Fatalf("a count below 10 must stay below 10: %v", r)
	}
}

// The usage figures come from the recording rules only; the raw series are read for the context alone.
func TestOnlyRecordedSeriesAndNoExtrapolation(t *testing.T) {
	runExport(t, map[string]string{"600 / max": vec(s("10", ""))})
	if len(queries) == 0 {
		t.Fatal("no queries")
	}
	var recorded, scrape, gaps bool
	for _, q := range queries {
		if strings.Contains(q, "increase(") || strings.Contains(q, "avg_over_time") || strings.Contains(q, "rate(") {
			t.Errorf("extrapolating function in %s", q)
		}
		for _, raw := range []string{"eduide_launch_total", "eduide_session_", "eduide_sessions", "eduide_pool_instances", "eduide_agent_requests", "container_", "kube_pod_container"} {
			if strings.Contains(q, raw) {
				t.Errorf("raw series %s in %s", raw, q)
			}
		}
		recorded = recorded || strings.Contains(q, "eduide:usage:")
		if strings.Contains(q, "up{") {
			if !strings.Contains(q, `container=~"operator|service|authz"`) && !strings.Contains(q, `job="kube-state-metrics"`) {
				t.Errorf("up query is not limited to the control plane: %s", q)
			}
			scrape = scrape || strings.Contains(q, "600 /")
			gaps = gaps || strings.Contains(q, "min(up")
		}
	}
	if !recorded || !scrape || !gaps {
		t.Errorf("queries missing: recorded=%v scrape=%v gaps=%v", recorded, scrape, gaps)
	}
}

func TestZeroLaunchesWriteARowAndRunningCarriesTheLaunchCount(t *testing.T) {
	dir, _ := runExport(t, map[string]string{
		"launches_per_minute": vec(s("0", `"template":"java"`)),
		"running_sessions":    vec(s("4", `"template":"java"`)),
	})
	rows := readRows(t, dir)
	l := find(rows, "launches", "java", "created")
	if l == nil || l["count"] != "0" {
		t.Fatalf("no launches must be a 0 row, not a missing row: %v", l)
	}
	r := find(rows, "running_sessions", "java", "Ready")
	if r == nil || r["count"] != "0" || r["suppressed"] != "true" || r["max"] != "" {
		t.Fatalf("running sessions row: %v", r)
	}
}

func TestRunningWithoutLaunchSeriesIsSuppressed(t *testing.T) {
	dir, _ := runExport(t, map[string]string{"running_sessions": vec(s("40", `"template":"java"`))})
	r := find(readRows(t, dir), "running_sessions", "java", "Ready")
	if r == nil || r["count"] != "" || r["suppressed"] != "true" || r["p95"] != "" {
		t.Fatalf("running sessions row: %v", r)
	}
}

func TestOnlyCompleteWeeksAndContextWithoutNamespace(t *testing.T) {
	srv, _ := fakeProm(t, nil)
	p, _ := newProm(srv.URL)
	from := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) // Wednesday of the second week
	dir := t.TempDir()
	if err := export(context.Background(), p, "eduide-test", "test", "3.0.0", "0.2.1", from, to, [2]time.Time{from, to}, false, now, dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "context.json"))
	var cf struct {
		Period struct {
			To      string   `json:"to"`
			Dropped []string `json:"dropped_incomplete_weeks"`
		} `json:"period"`
	}
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	if len(cf.Period.Dropped) != 1 || cf.Period.Dropped[0] != "2026-W47" {
		t.Fatalf("incomplete week not dropped: %s", b)
	}
	if strings.Contains(string(b), `"namespace"`) || strings.Contains(string(b), "eduide-test") {
		t.Fatalf("context.json names the namespace: %s", b)
	}
	if !strings.Contains(string(b), "Only complete ISO weeks") {
		t.Fatalf("context.json does not say how the period is rounded: %s", b)
	}
}

func TestWeekStart(t *testing.T) {
	for in, want := range map[string]string{"2026-11-09": "2026-11-09", "2026-11-14": "2026-11-09", "2026-11-15": "2026-11-09", "2026-11-16": "2026-11-16"} {
		d, _ := time.Parse("2006-01-02", in)
		if got := weekStart(d).Format("2006-01-02"); got != want {
			t.Errorf("weekStart(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestMissingSeriesAreListedPerWeek(t *testing.T) {
	dir, _ := runExport(t, nil)
	b, _ := os.ReadFile(filepath.Join(dir, "context.json"))
	if !strings.Contains(string(b), `"missing_series_by_week"`) || !strings.Contains(string(b), `"2026-W46"`) {
		t.Fatalf("missing series are not listed per week: %s", b)
	}
}

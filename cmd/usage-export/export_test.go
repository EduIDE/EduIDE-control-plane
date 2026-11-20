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

// fakeProm answers a query with the first canned response whose key is a substring of the expression;
// everything else is an empty vector. It records the paths it was asked for.
func fakeProm(t *testing.T, canned map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		q := r.URL.Query().Get("query")
		for k, v := range canned {
			if strings.Contains(q, k) {
				_, _ = w.Write([]byte(v))
				return
			}
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
	if err := export(context.Background(), p, "eduide-test", "test", from, to, to.AddDate(0, 0, 3), dir); err != nil {
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
		"session_startup_seconds_count": vec(
			s("40", `"template":"java","start_mode":"eager","result":"ready"`),
			s("2", `"template":"java","start_mode":"eager","result":"failed"`),
			s("8", `"template":"java","start_mode":"lazy_fallback","result":"ready"`),
			s("3", `"template":"py","start_mode":"lazy","result":"ready"`)),
		"session_startup_seconds_bucket": vec(
			bucket("java", "eager", "1", "10"), bucket("java", "eager", "2", "30"), bucket("java", "eager", "+Inf", "40"),
			bucket("java", "lazy_fallback", "10", "8"), bucket("java", "lazy_fallback", "+Inf", "8"),
			bucket("py", "lazy", "10", "3"), bucket("py", "lazy", "+Inf", "3")),
		"eduide_launch_total":   vec(s("50", `"template":"java"`)),
		"eduide_pool_instances": vec(s("2", `"pool":"java","state":"idle_ready"`)),
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
		"eduide_launch_total": vec(s("50", `"template":"java"`)),
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
	}{{9, true}, {10, false}, {0, true}, {math.NaN(), false}} {
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

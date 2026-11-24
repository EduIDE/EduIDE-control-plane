package main

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readGz(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestArchiveDays(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	// Day 21 has samples, day 22 fails, day 23 is already archived, day 20 has none.
	matrix := `{"status":"success","data":{"resultType":"matrix","result":[
	  {"metric":{"__name__":"eduide:usage:launches_per_minute","namespace":"ns","template":"java"},"values":[[1795219260,"2"],[1795219320,"0"]]},
	  {"metric":{"__name__":"eduide:usage:running_sessions","namespace":"ns","template":"c"},"values":[[1795219260,"1"]]},
	  {"metric":{"__name__":"eduide:usage:launches_per_minute","namespace":"ns","template":"c"},"values":[[1795219260,"1"]]}]}}`
	var times []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !strings.Contains(q.Get("query"), `{__name__=~"eduide:usage:.+", namespace="ns"}[1d]`) {
			t.Errorf("unexpected query %q", q.Get("query"))
		}
		times = append(times, q.Get("time"))
		switch q.Get("time") {
		case "1795305600": // end of 2026-11-21
			_, _ = w.Write([]byte(matrix))
		case "1795392000": // end of 2026-11-22
			_, _ = w.Write([]byte(`{"status":"error","error":"boom"}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		}
	}))
	defer srv.Close()
	if got := day(22).Unix(); got != 1795305600 {
		t.Fatalf("test clock: end of the 21st is %d", got)
	}

	dir := t.TempDir()
	done := filepath.Join(dir, "ns", "2026-11-23.om.gz")
	if err := os.MkdirAll(filepath.Dir(done), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(done, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := newProm(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = archiveDays(context.Background(), p, "ns", dir, 4, day(24).Add(3*time.Hour))
	if err == nil || !strings.Contains(err.Error(), "2026-11-22") {
		t.Fatalf("want the error of the 22nd, got %v", err)
	}
	if strings.Join(times, ",") != "1795219200,1795305600,1795392000" {
		t.Errorf("queried %v, want the ends of the 20th, 21st and 22nd only", times)
	}
	if b, _ := os.ReadFile(done); string(b) != "kept" { //nolint:gosec // test file
		t.Error("an archived day was written again")
	}
	for _, d := range []string{"2026-11-20", "2026-11-22"} {
		if _, err := os.Stat(filepath.Join(dir, "ns", d+".om.gz")); err == nil {
			t.Errorf("%s has a file, want none (no samples or failed)", d)
		}
	}
	got := readGz(t, filepath.Join(dir, "ns", "2026-11-21.om.gz"))
	want := `# TYPE eduide:usage:launches_per_minute gauge
eduide:usage:launches_per_minute{namespace="ns",template="c"} 1 1795219260
eduide:usage:launches_per_minute{namespace="ns",template="java"} 2 1795219260
eduide:usage:launches_per_minute{namespace="ns",template="java"} 0 1795219320
# TYPE eduide:usage:running_sessions gauge
eduide:usage:running_sessions{namespace="ns",template="c"} 1 1795219260
# EOF
`
	if got != want {
		t.Errorf("archive:\n%s\nwant:\n%s", got, want)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "ns", ".tmp-*"))
	if len(leftovers) > 0 {
		t.Errorf("temp files left: %v", leftovers)
	}

	// The file must load back into a TSDB, which is the point of keeping it.
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not installed")
	}
	om := filepath.Join(t.TempDir(), "day.om")
	if err := os.WriteFile(om, []byte(got), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "tsdb", "create-blocks-from", "openmetrics", om, t.TempDir()).CombinedOutput() //nolint:gosec // fixed binary and a temp file
	if err != nil {
		t.Fatalf("promtool cannot load the archive: %v\n%s", err, out)
	}
}

// An empty yesterday means the rules do not reach this Prometheus: the run must fail, not stay green.
// An empty file left by a crash is archived again.
func TestArchiveDaysEmptyYesterdayAndEmptyFile(t *testing.T) {
	day21end := "1795305600"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("time") == day21end {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"eduide:usage:x","namespace":"ns","template":"a\"b\\c\nd"},"values":[[1795219260,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	empty := filepath.Join(dir, "ns", "2026-11-21.om.gz")
	if err := os.MkdirAll(filepath.Dir(empty), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := newProm(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = archiveDays(context.Background(), p, "ns", dir, 2, time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "2026-11-22: no eduide:usage:* samples") {
		t.Fatalf("want an error for the empty yesterday, got %v", err)
	}
	got := readGz(t, empty)
	if !strings.Contains(got, `eduide:usage:x{namespace="ns",template="a\"b\\c\nd"} 1 1795219260`) {
		t.Errorf("empty file not rewritten or label not escaped the OpenMetrics way:\n%s", got)
	}
}

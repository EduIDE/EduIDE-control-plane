package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDeltaSemantics runs the delta expression through promtool's rule unit tests, because a fake HTTP
// server cannot evaluate PromQL. It covers a series that starts inside the window (increase() drops its
// first increment), a counter reset, two pods and a series that is idle. A series that is first seen with a
// value above zero counts from zero: that holds for pods, which start their counters at zero.
// Skipped without promtool.
func TestDeltaSemantics(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not installed")
	}
	dir := t.TempDir()
	rules := fmt.Sprintf("groups:\n- name: t\n  rules:\n  - record: test:delta\n    expr: sum by (g) (%s)\n  - record: test:delta_peak\n    expr: max_over_time(sum by (g) (%s)[300s:30s])\n",
		delta("c", "", "300s"), delta("c", "", "60s"))
	// Samples every 30 s from t=0 to t=600 s.
	tests := `rule_files: [rules.yml]
evaluation_interval: 30s
tests:
- interval: 30s
  input_series:
  # existed before the window: 5 at t=300, 8 at t=600 -> 3
  - series: 'c{g="x", pod="a"}'
    values: '5x10 6 6 6 7 7 7 8 8 8 8'
  # starts inside the window with its first increment: 1 at t=450, 4 at t=600 -> 4
  - series: 'c{g="x", pod="b"}'
    values: '_x15 1 2 3 3 4 4'
  # counter reset: 10 at t=300, 2 at t=600 -> 2
  - series: 'c{g="x", pod="c"}'
    values: '10x10 2x10'
  # idle series: no increase -> 0 and still present
  - series: 'c{g="y", pod="d"}'
    values: '7x20'
  promql_expr_test:
  - expr: test:delta
    eval_time: 10m
    exp_samples:
    - labels: 'test:delta{g="x"}'
      value: 9
    - labels: 'test:delta{g="y"}'
      value: 0
  # largest 60 s window after all series exist: the step at t=330 holds pod a 5 to 6 and the reset of pod c (2, counted from zero)
  - expr: test:delta_peak
    eval_time: 10m
    exp_samples:
    - labels: 'test:delta_peak{g="x"}'
      value: 3
    - labels: 'test:delta_peak{g="y"}'
      value: 0
`
	for name, body := range map[string]string{"rules.yml": rules, "test.yml": tests} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(bin, "test", "rules", filepath.Join(dir, "test.yml")).CombinedOutput() //nolint:gosec // fixed binary and a temp file
	if err != nil {
		t.Fatalf("promtool: %v\n%s", err, out)
	}
}

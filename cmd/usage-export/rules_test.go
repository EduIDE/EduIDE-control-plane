package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUsageRules runs the recording rules of the chart through promtool's rule unit tests
// (testdata/usage-rules-*.test.yml), because a fake HTTP server cannot evaluate PromQL.
// promtool evaluates every group at the evaluation_interval of the test file, not at the group interval,
// so each rule group has its own test file. Skipped without promtool.
func TestUsageRules(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not installed")
	}
	src, err := os.ReadFile("../../charts/eduide-control-plane/files/usage-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rules := "groups:\n" + strings.ReplaceAll(string(src), "__NAMESPACE__", "ns")
	if err := os.WriteFile(filepath.Join(dir, "rules.yml"), []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"1m", "5m"} {
		test, err := os.ReadFile("testdata/usage-rules-" + group + ".test.yml")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, group+".test.yml")
		if err := os.WriteFile(path, test, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, "test", "rules", path).CombinedOutput() //nolint:gosec // fixed binary and a temp file
		if err != nil {
			t.Fatalf("promtool, %s group: %v\n%s", group, err, out)
		}
	}
}

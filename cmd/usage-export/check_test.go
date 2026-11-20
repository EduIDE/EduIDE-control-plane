package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDir(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		bad  bool
	}{
		"clean csv":     {"2026-W46,2026-11-09,7,startup,java,eager,seconds,42,false,1.5", false},
		"timestamp":     {"generated 2026-11-14T10:00:00Z", false},
		"version":       {"schema_version 1, scrape 10", false},
		"email":         {"owner,ab12@example.org", true},
		"ipv4":          {"node 10.1.2.3", true},
		"ipv6":          {"addr 2001:db8::1", true},
		"ipv6 loopback": {"addr ::1", true},
		"pod":           {"pod ide-mdraa5ztvznkcxtdxwaq", true},
		"template":      {"template ide-python", false},
		"session":       {"session s-k3hgk5pi4r", true},
		"hash":          {"owner 4c0a11dda45d2f75", true},
		"host":          {"host ide.example.org", true},
		"file names":    {"see weekly.csv and context.json", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "x.csv"), []byte("h\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			found, err := checkDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if (len(found) > 0) != tc.bad {
				t.Fatalf("%q: findings %v", tc.line, found)
			}
			if tc.bad && !strings.Contains(found[0], "x.csv:2:") {
				t.Fatalf("finding lacks file and line: %v", found)
			}
		})
	}
}

func TestRunCheckFailsOnFindings(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"owner":"x@y.org"}`), 0o600)
	if err := run([]string{"--check", dir}); err == nil {
		t.Fatal("check passed on an e-mail address")
	}
}

func TestCheckSmallCells(t *testing.T) {
	head := strings.Join(csvHeader, ",") + "\n"
	for name, tc := range map[string]struct {
		row string
		bad bool
	}{
		"suppressed":          {"2026-W46,2026-11-09,7,startup,java,eager,seconds,3,true,,,,,,,,,", false},
		"large cell":          {"2026-W46,2026-11-09,7,startup,java,eager,seconds,12,false,,1.5,2,3,,,,,", false},
		"small, not flagged":  {"2026-W46,2026-11-09,7,startup,java,eager,seconds,3,false,,,,,,,,,", true},
		"small, p95 filled":   {"2026-W46,2026-11-09,7,startup,java,eager,seconds,3,true,,,2,,,,,,", true},
		"small, peak filled":  {"2026-W46,2026-11-09,7,launches,java,created,launches,3,true,,,,,,,5,,", true},
		"no count, not supp.": {"2026-W46,2026-11-09,7,running_sessions,java,Ready,sessions,,false,,,3,,,,,,", true},
		"no count, supp.":     {"2026-W46,2026-11-09,7,running_sessions,java,Ready,sessions,,true,,,,,,,,,", false},
		"pool row":            {"2026-W46,2026-11-09,7,pool_slot_hours,java,idle_ready,slot_hours,,false,12,,,,,,,,", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "weekly.csv"), []byte(head+tc.row+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			found, err := checkDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if (len(found) > 0) != tc.bad {
				t.Fatalf("findings %v", found)
			}
		})
	}
}

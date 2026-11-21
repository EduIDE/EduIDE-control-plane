package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// identifiers are the patterns an export must not contain (brief of the usage metrics, constraint 1).
var identifiers = []struct {
	name string
	re   *regexp.Regexp
}{
	{"e-mail address", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`)},
	{"IPv4 address", regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)},
	{"IPv6 address", regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){4,7}[0-9a-f]{1,4}\b|(?:[0-9a-f]{0,4}:){1,6}:[0-9a-f]{0,4}\b`)},
	{"pod name", regexp.MustCompile(`\bide-[a-z0-9]{12,}`)},
	{"session name", regexp.MustCompile(`\bs-[a-z0-9]{8,}\b`)},
	{"hex hash", regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`)},
	{"host name", regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+(?:de|com|org|net|edu|io|eu|cloud|local|internal|cluster|svc|lan)\b`)},
}

// checkDir scans every file below dir and returns one message per finding.
func checkDir(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fh, err := os.Open(path) //nolint:gosec // path comes from walking the directory the user named
		if err != nil {
			return err
		}
		defer func() { _ = fh.Close() }()
		if filepath.Base(path) == "weekly.csv" {
			found = append(found, checkCells(path)...)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for n := 1; sc.Scan(); n++ {
			for _, id := range identifiers {
				if id.re.MatchString(sc.Text()) {
					found = append(found, fmt.Sprintf("%s:%d: looks like a %s", path, n, id.name))
				}
			}
		}
		return sc.Err()
	})
	return found, err
}

// noCountMetrics are not per-session figures; their rows have no count and need none.
var noCountMetrics = map[string]bool{"restarts": true, "oom_kills": true}

// exempt tells whether a row is not a per-session figure. Hours of idle pool slots do not depend on sessions;
// hours of claimed slots and of empty pools do, so they need a count like any other row.
func exempt(metric, dimension string) bool {
	return noCountMetrics[metric] || (metric == "pool_slot_hours" && dimension != "claimed")
}

// distColumns must be empty in a small cell.
var distColumns = []string{"value", "p50", "p95", "max", "failure_share", "peak_hour", "peak_minute", "max_20s", "max_60s"}

// checkCells verifies the small-cell rule of a weekly.csv: below 10 sessions only the count may be filled and suppressed must be true.
func checkCells(path string) []string {
	fh, err := os.Open(path) //nolint:gosec // path comes from walking the directory the user named
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	defer func() { _ = fh.Close() }()
	recs, err := csv.NewReader(fh).ReadAll()
	if err != nil || len(recs) == 0 {
		return []string{fmt.Sprintf("%s: cannot read the CSV", path)}
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	var out []string
	for n, r := range recs[1:] {
		get := func(c string) string { return r[col[c]] }
		fail := func(why string) { out = append(out, fmt.Sprintf("%s:%d: small-cell rule: %s", path, n+2, why)) }
		count := get("count")
		if count == "" {
			if !exempt(get("metric"), get("dimension")) && get("suppressed") != "true" {
				fail("row without a count is not suppressed")
			}
			continue
		}
		v, err := strconv.ParseFloat(count, 64)
		if err != nil || v >= minCell {
			continue
		}
		if get("suppressed") != "true" {
			fail("count below 10 but suppressed is not true")
		}
		for _, c := range distColumns {
			if get(c) != "" {
				fail("count below 10 but " + c + " is filled")
			}
		}
	}
	return out
}

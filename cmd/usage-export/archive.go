package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// archiveDays keeps the raw samples of every recorded series eduide:usage:* of the namespace, one gzipped
// OpenMetrics file per UTC day, so they outlive the Prometheus retention. Days that already have a file are
// skipped; days without samples get no file and are tried again on the next run, but yesterday without samples is
// an error: then the rules do not reach this Prometheus and the archive would stay empty unnoticed. The files load
// back with `promtool tsdb create-blocks-from openmetrics`.
func archiveDays(ctx context.Context, p *prom, ns, dir string, days int, now time.Time) error {
	today := now.UTC().Truncate(24 * time.Hour)
	var errs []error
	for i := days; i >= 1; i-- {
		day := today.AddDate(0, 0, -i)
		path := filepath.Join(dir, ns, day.Format("2006-01-02")+".om.gz")
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			continue
		}
		n, err := archiveDay(ctx, p, ns, day, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", day.Format("2006-01-02"), err))
			continue
		}
		if n > 0 {
			fmt.Printf("archived %s: %d samples\n", day.Format("2006-01-02"), n)
		} else if i == 1 {
			errs = append(errs, fmt.Errorf("%s: no eduide:usage:* samples for namespace %s; are the recording rules loaded by this Prometheus?", day.Format("2006-01-02"), ns))
		}
	}
	return errors.Join(errs...)
}

// archiveDay writes the samples of the UTC day and returns their number. The range ends at day+1 00:00; on
// Prometheus 2.x both ends are inclusive, which is harmless because rule evaluations never fall exactly on midnight
// and duplicate identical samples load fine.
func archiveDay(ctx context.Context, p *prom, ns string, day time.Time, path string) (int, error) {
	expr := fmt.Sprintf(`{__name__=~"eduide:usage:.+", namespace=%q}[1d]`, ns)
	items, err := p.do(ctx, "archive", expr, day.AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		n += len(it.Values)
	}
	if n == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the archive is aggregated data, meant to be shared
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	zw := gzip.NewWriter(tmp)
	if err := writeOpenMetrics(zw, items); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	// Synced before the rename: after a power loss the day is either complete or missing, never an empty file.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	defer func() { _ = d.Close() }()
	return n, d.Sync()
}

// omEscape escapes label values the way OpenMetrics does; strconv.Quote would add Go escapes it does not know.
var omEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// writeOpenMetrics writes a range vector as OpenMetrics text, grouped by metric family as the format requires.
func writeOpenMetrics(w io.Writer, items []rawItem) error {
	type series struct {
		name, labels string
		values       [][2]any
	}
	ss := make([]series, 0, len(items))
	for _, it := range items {
		keys := make([]string, 0, len(it.Metric))
		for k := range it.Metric {
			if k != "__name__" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + `="` + omEscape.Replace(it.Metric[k]) + `"`
		}
		ss = append(ss, series{it.Metric["__name__"], strings.Join(parts, ","), it.Values})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].name != ss[j].name {
			return ss[i].name < ss[j].name
		}
		return ss[i].labels < ss[j].labels
	})
	var b strings.Builder
	last := ""
	for _, s := range ss {
		if s.name != last {
			fmt.Fprintf(&b, "# TYPE %s gauge\n", s.name)
			last = s.name
		}
		for _, v := range s.values {
			ts, ok1 := v[0].(float64)
			val, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				return fmt.Errorf("unexpected sample %v in %s", v, s.name)
			}
			fmt.Fprintf(&b, "%s{%s} %s %s\n", s.name, s.labels, val, strconv.FormatFloat(ts, 'f', -1, 64))
		}
	}
	b.WriteString("# EOF\n")
	_, err := io.WriteString(w, b.String())
	return err
}

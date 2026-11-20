// Command usage-export writes the aggregated usage data of one installation (docs/operations/usage-export.md).
// It reads only through the Prometheus HTTP API and runs a fixed list of queries.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed README.md
var readme []byte

var safeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "usage-export:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("usage-export", flag.ContinueOnError)
	promURL := fs.String("prometheus", "", "Prometheus base URL")
	ns := fs.String("namespace", "", "namespace of the control plane and the session pods")
	install := fs.String("installation", "", "installation name, used for the output directory")
	fromS := fs.String("from", "", "first day, YYYY-MM-DD (default: 6 days before --to)")
	toS := fs.String("to", "", "last day, YYYY-MM-DD, inclusive (default: today)")
	out := fs.String("out", "", "output directory")
	check := fs.String("check", "", "scan this directory for identifiers and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check != "" {
		found, err := checkDir(*check)
		if err != nil {
			return err
		}
		for _, m := range found {
			fmt.Fprintln(os.Stderr, m)
		}
		if len(found) > 0 {
			return fmt.Errorf("%d findings", len(found))
		}
		fmt.Println("no identifiers found")
		return nil
	}
	if *promURL == "" || *out == "" || !safeName.MatchString(*ns) || !safeName.MatchString(*install) {
		return fmt.Errorf("--prometheus, --out, --namespace and --installation are required; names use a-z, 0-9 and -")
	}
	now := time.Now().UTC()
	to, err := parseDay(*toS, now.Truncate(24*time.Hour))
	if err != nil {
		return err
	}
	from, err := parseDay(*fromS, to.AddDate(0, 0, -6))
	if err != nil {
		return err
	}
	if from.After(to) {
		return fmt.Errorf("--from is after --to")
	}
	p, err := newProm(*promURL)
	if err != nil {
		return err
	}
	dir := filepath.Join(*out, *install, from.Format("2006-01-02")+"_"+to.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the export is meant to be shared
		return err
	}
	return export(context.Background(), p, *ns, *install, from, to, now, dir)
}

func parseDay(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	return time.Parse("2006-01-02", s)
}

func export(ctx context.Context, p *prom, ns, install string, from, to, now time.Time, dir string) error {
	end := to.AddDate(0, 0, 1)
	if end.After(now) {
		end = now.Truncate(time.Minute)
	}
	c := &collector{p: p, ns: ns}
	cf := c.context(ctx, install, from, to, end)
	if c.lastErr != nil {
		return c.lastErr
	}
	for _, w := range weeks(from, end) {
		c.collectWeek(ctx, w)
	}
	if c.lastErr != nil {
		return c.lastErr
	}
	cf.MissingSeries = p.missing()
	if err := writeCSV(filepath.Join(dir, "weekly.csv"), c.rows); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "context.json"), cf); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), readme, 0o644); err != nil { //nolint:gosec // the export is meant to be shared
		return err
	}
	fmt.Printf("wrote %d rows to %s (missing series: %d)\n", len(c.rows), dir, len(cf.MissingSeries))
	return nil
}

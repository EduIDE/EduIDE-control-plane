// Command usage-export writes the aggregated usage data of one installation (docs/operations/usage-export.md).
// It reads only through the Prometheus HTTP API and runs a fixed list of queries.
package main

import (
	"context"
	_ "embed"
	"errors"
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
	release := fs.String("release", "", "EduIDE release of the installation, for example 3.0.0 (required)")
	chart := fs.String("chart-version", "", "version of the eduide-control-plane chart (required)")
	out := fs.String("out", "", "output directory")
	partial := fs.Bool("partial-weeks", false, "also export the incomplete current week (rows carry days below 7)")
	check := fs.String("check", "", "scan this directory for identifiers and exit")
	archive := fs.String("archive", "", "keep the raw samples of eduide:usage:* in this directory, one file per day")
	archiveN := fs.Int("archive-days", 7, "with --archive: archive the missing days among the last N days")
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
	var archErr error
	if *archive != "" {
		if *promURL == "" || !safeName.MatchString(*ns) || *archiveN < 1 {
			return fmt.Errorf("--archive needs --prometheus, --namespace and --archive-days of at least 1")
		}
		ap, err := newProm(*promURL)
		if err != nil {
			return err
		}
		archErr = archiveDays(context.Background(), ap, *ns, *archive, *archiveN, time.Now())
		if *out == "" {
			return archErr
		}
	}
	if *release == "" || *chart == "" {
		return fmt.Errorf("--release and --chart-version are required")
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
	reqFrom, reqTo := from, to
	from, to = weekStart(from), weekStart(to).AddDate(0, 0, 6)
	p, err := newProm(*promURL)
	if err != nil {
		return err
	}
	period := [2]time.Time{reqFrom, reqTo}
	dir := filepath.Join(*out, *install, from.Format("2006-01-02")+"_"+to.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the export is meant to be shared
		return err
	}
	// The weekly export runs even when a day could not be archived; both errors are reported.
	return errors.Join(archErr, export(context.Background(), p, *ns, *install, *release, *chart, from, to, period, *partial, now, dir))
}

func parseDay(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	return time.Parse("2006-01-02", s)
}

func export(ctx context.Context, p *prom, ns, install, release, chart string, from, to time.Time, period [2]time.Time, partial bool, now time.Time, dir string) error {
	end := to.AddDate(0, 0, 1)
	var dropped []string
	ws := weeks(from, end)
	if partial {
		if end.After(now) {
			end = now.Truncate(time.Minute)
			ws = weeks(from, end)
		}
	} else {
		var whole []window
		for _, w := range ws {
			if w.end.After(now) {
				dropped = append(dropped, w.label())
				continue
			}
			whole = append(whole, w)
		}
		ws = whole
	}
	if len(ws) > 0 {
		end = ws[len(ws)-1].end
	}
	c := &collector{p: p, ns: ns}
	cf := c.context(ctx, install, release, chart, from, to, end)
	cf.Period.RequestedFrom, cf.Period.RequestedTo = period[0].Format("2006-01-02"), period[1].Format("2006-01-02")
	cf.Period.Note = "Only complete ISO weeks are exported (Monday 00:00 to Sunday 24:00 UTC); the requested days are rounded to week boundaries."
	if partial {
		cf.Period.Note = "Weeks are ISO weeks (Monday 00:00 UTC); with partial weeks the last week may be incomplete, see days in weekly.csv."
	}
	cf.Period.DroppedWeeks = append([]string{}, dropped...)
	if c.lastErr != nil {
		return c.lastErr
	}
	for _, w := range ws {
		c.collectWeek(ctx, w)
	}
	if c.lastErr != nil {
		return c.lastErr
	}
	cf.MissingSeries = p.missing()
	cf.MissingByWeek = p.missingByWeek()
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

// weekStart is the Monday 00:00 UTC of the ISO week of t.
func weekStart(t time.Time) time.Time {
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

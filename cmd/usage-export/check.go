package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

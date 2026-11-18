// Package logx holds the logging helpers every binary shares. It uses the standard library only,
// so the agent can import it without pulling in the telemetry dependencies.
package logx

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Level reads LOG_LEVEL (debug, info or warn); anything else means info.
func Level() slog.Level { return ParseLevel(os.Getenv("LOG_LEVEL")) }

// ParseLevel maps debug, info or warn to a level; anything else means info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// New returns a JSON logger on stdout that tags every line with the service name.
func New(service string) *slog.Logger { return NewAt(service, Level()) }

// NewAt is New with an explicit level.
func NewAt(service string, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", service)
}

// BuildVersion is set at link time (-ldflags "-X .../internal/logx.BuildVersion=..."), because image
// builds run without .git and have no VCS data.
var BuildVersion string

// Version is BuildVersion when set, else the VCS revision the binary was built from ("-dirty" when the
// tree had changes), else the module version.
func Version() string {
	if BuildVersion != "" {
		return BuildVersion
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(12, len(s.Value))]
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return bi.Main.Version
	}
	return rev + dirty
}

// Limiter lets at most max lines per key through each minute and counts the rest.
type Limiter struct {
	max int
	now func() time.Time

	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	start      time.Time
	n, dropped int
}

// NewLimiter allows max lines per key and minute. The keys must come from a small fixed set.
func NewLimiter(max int) *Limiter {
	return &Limiter{max: max, now: time.Now, m: map[string]*bucket{}}
}

// Allow reports whether a line for key may be logged. The first line of a new minute also returns
// how many lines of the previous minute were dropped, so the log shows that lines are missing.
func (l *Limiter) Allow(key string) (ok bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.m[key]
	if b == nil {
		b = &bucket{start: now}
		l.m[key] = b
	}
	if now.Sub(b.start) >= time.Minute {
		suppressed = b.dropped
		*b = bucket{start: now}
	}
	if b.n >= l.max {
		b.dropped++
		return false, 0
	}
	b.n++
	return true, suppressed
}

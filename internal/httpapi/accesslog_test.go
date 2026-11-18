package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs swaps the default logger for a JSON logger at level.
func captureLogs(t *testing.T, level slog.Level) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func TestAccessLog(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)
	e := newEnv(t, session("s-a", "alice", "java", "Ready"))
	e.do("GET", "/sessions/s-a", "alice", nil)
	e.do("GET", "/sessions/s-a", "bob", nil)
	e.do("GET", "/sessions/s-a", "", nil)
	status, _, r := openStream(t, e, "/sessions/s-a/events", "alice")
	if status != 200 {
		t.Fatal(status)
	}
	for {
		if _, ok := r.next(t); !ok {
			break
		}
	}
	out := buf.String()
	for _, want := range []string{
		`"msg":"request"`, `"method":"GET"`, `"route":"/api/v1/sessions/{name}"`, `"status":200`, `"status":404`, `"status":401`,
		`"duration_ms":`, `"bytes":`, `"user":"`, `"msg":"stream open"`, `"msg":"stream close"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"alice", "bob", `"level":"ERROR"`, "Bearer"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
}

func TestAccessLogServerErrorIsAnError(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)
	e := newEnv(t)
	h := e.srv.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, problem(CodeInternal, "internal error"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	out := buf.String()
	for _, want := range []string{`"level":"ERROR"`, `"status":500`, `"error":"`, `"route":"unmatched"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
}

func TestDebugLinesOnlyAtDebugLevel(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		debug bool
	}{{slog.LevelInfo, false}, {slog.LevelDebug, true}} {
		buf := captureLogs(t, tc.level)
		e := newEnv(t)
		if rec := e.do("POST", "/sessions", "alice", launchBody(nil)); rec.Code != 201 {
			t.Fatal(rec.Body)
		}
		e.do("POST", "/sessions", "alice", launchBody(nil)) // idempotent hit
		e.do("POST", "/sessions", "alice", map[string]any{"template": "java", "data": map[string]string{"NOPE": "v"}})
		out := buf.String()
		for _, want := range []string{`"msg":"launch validated"`, `"msg":"launch rate limit"`, `"msg":"launch rejected"`, "idempotent"} {
			if got := strings.Contains(out, want); got != tc.debug {
				t.Errorf("level %v: %s present=%v, want %v\n%s", tc.level, want, got, tc.debug, out)
			}
		}
		if strings.Contains(out, `"level":"DEBUG"`) != tc.debug {
			t.Errorf("level %v: wrong debug presence", tc.level)
		}
		for _, bad := range []string{"secret-token-value", gitURI, "alice"} {
			if strings.Contains(out, bad) {
				t.Errorf("level %v: log contains %q", tc.level, bad)
			}
		}
	}
}

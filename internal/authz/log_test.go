package authz

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestDecisionLog(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{by: map[string][]v1alpha1.IDESession{
		instanceA: {session("alice", instanceA, v1alpha1.SessionReady, false)},
	}})
	s.LogPerMinute = 2
	good := idCookie + "=" + iss.token(t, nil, "", "", "alice", 0)
	mallory := idCookie + "=" + iss.token(t, nil, "", "", "mallory", 0)
	junk := idCookie + "=junk-token-value"

	run := func(level slog.Level) string {
		buf := captureLogs(t, level)
		for _, c := range []string{good, good, mallory, mallory, mallory, mallory, junk} {
			if _, err := s.Check(context.Background(), req("/"+instanceA+"/", c)); err != nil {
				t.Fatal(err)
			}
		}
		return buf.String()
	}

	info := run(slog.LevelInfo)
	if n := strings.Count(info, `"reason":"not-owner"`); n != 2 {
		t.Errorf("not-owner lines: %d, want 2 (rate limited)\n%s", n, info)
	}
	for _, want := range []string{`"msg":"authz decision"`, `"result":"deny"`, `"result":"login"`, `"reason":"invalid-token"`, `"user":"`, `"duration_ms":`} {
		if !strings.Contains(info, want) {
			t.Errorf("info log lacks %s\n%s", want, info)
		}
	}
	if strings.Contains(info, `"result":"allow"`) || strings.Contains(info, `"level":"DEBUG"`) {
		t.Errorf("allow or debug at info level:\n%s", info)
	}

	debug := run(slog.LevelDebug)
	if !strings.Contains(debug, `"result":"allow"`) || !strings.Contains(debug, `"reason":"owner"`) {
		t.Errorf("debug log lacks the allow line:\n%s", debug)
	}
	for _, out := range []string{info, debug} {
		for _, bad := range []string{"alice", "mallory", "junk-token-value", idCookie + "=", instanceA, good[len(idCookie)+1:]} {
			if strings.Contains(out, bad) {
				t.Errorf("log contains %q:\n%s", bad, out)
			}
		}
	}
}

func TestDecisionLogIsRateLimited(t *testing.T) {
	iss := newIssuer(t)
	s := newServer(t, iss, fakeSessions{})
	s.LogPerMinute = 1
	buf := captureLogs(t, slog.LevelInfo)
	for range 3 {
		_, _ = s.Check(context.Background(), req("x", ""))
	}
	if n := strings.Count(buf.String(), `"reason":"bad-path"`); n != 1 {
		t.Fatalf("%d lines, want 1:\n%s", n, buf)
	}
}

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

func TestStartReadyShutdownLines(t *testing.T) {
	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("agent-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TOKEN_FILE", tokFile)
	t.Setenv("AGENT_PROXY_ADDR", freeAddr(t))
	t.Setenv("AGENT_CONTROL_ADDR", freeAddr(t))
	t.Setenv("AGENT_LOCAL_ADDR", freeAddr(t))
	buf := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- run(slog.New(slog.NewJSONHandler(buf, nil))) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"ready"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"starting"`, `"version":`, `"proxy_addr":`, `"control_addr":`, `"msg":"ready"`, `"msg":"shutdown"`, `"reason":"signal"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "agent-token-value") {
		t.Errorf("log contains the token:\n%s", out)
	}
}

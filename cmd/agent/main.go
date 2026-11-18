// Command agent is the session pod sidecar (ADR 0006).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agent"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// idleTimeout bounds idle keep-alive connections; without it they stay open forever.
const idleTimeout = 5 * time.Minute

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func run(log *slog.Logger) error {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(8 << 20)
	}
	raw, err := os.ReadFile(env("AGENT_TOKEN_FILE", "/var/run/eduide/token"))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return errors.New("token file is empty")
	}
	localAddr := env("AGENT_LOCAL_ADDR", "127.0.0.1:8082")
	if !loopback(localAddr) {
		return errors.New("AGENT_LOCAL_ADDR must be a loopback address")
	}
	a := agent.New(token, env("AGENT_IDE_PORT", "3000"), log)

	servers := []*http.Server{
		{Addr: env("AGENT_PROXY_ADDR", "0.0.0.0:8080"), Handler: a.ProxyHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout},
		{Addr: env("AGENT_CONTROL_ADDR", "0.0.0.0:8081"), Handler: a.ControlHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: idleTimeout},
		{Addr: localAddr, Handler: a.LocalHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() { errc <- s.ListenAndServe() }()
	}
	log.Info("agent started")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return nil
}

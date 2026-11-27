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
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
)

func main() {
	// "noop" exits at once; the prepull DaemonSet uses it to pull the image onto every session node.
	if len(os.Args) > 1 && os.Args[1] == "noop" {
		return
	}
	// AGENT_LOG_LEVEL is set by the operator from the config; LOG_LEVEL works for a manual run.
	log := logx.NewAt("eduide-agent", logx.ParseLevel(env("AGENT_LOG_LEVEL", os.Getenv("LOG_LEVEL"))))
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

// servers returns the proxy, control and local server and the connection limit of each.
func servers(a *agent.Agent, localAddr string) ([]*http.Server, []int) {
	return []*http.Server{
		{Addr: env("AGENT_PROXY_ADDR", "0.0.0.0:8080"), Handler: a.ProxyHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout, MaxHeaderBytes: proxyHeaderBytes},
		{Addr: env("AGENT_CONTROL_ADDR", "0.0.0.0:8081"), Handler: a.ControlHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: idleTimeout, MaxHeaderBytes: smallHeaderBytes},
		{Addr: localAddr, Handler: a.LocalHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout, MaxHeaderBytes: smallHeaderBytes},
	}, []int{proxyConns, smallConns, smallConns}
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
	a.PathPrefix = os.Getenv("AGENT_PATH_PREFIX")

	// Set by the operator only with Git accounts (ADR 0023); without it /git/ answers 404.
	if u := os.Getenv("AGENT_GIT_PROXY_URL"); u != "" {
		hosts := strings.FieldsFunc(os.Getenv("AGENT_GIT_HOSTS"), func(r rune) bool { return r == ',' })
		if err := a.EnableGit(u, hosts, os.Getenv("AGENT_GIT_LANDING_URL")); err != nil {
			return err
		}
	}

	if c := os.Getenv("AGENT_ID_TOKEN_COOKIE"); c != "" {
		agent.StripCookie(c)
	}

	servers, conns := servers(a, localAddr)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, len(servers))
	for i, s := range servers {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.Addr)
		if err != nil {
			return err
		}
		go func() { errc <- s.Serve(limitListener(ln, conns[i])) }()
	}
	log.Info("starting", "version", logx.Version(), "proxy_addr", servers[0].Addr, "control_addr", servers[1].Addr,
		"local_addr", localAddr, "ide_port", env("AGENT_IDE_PORT", "3000"), "log_level", logx.ParseLevel(env("AGENT_LOG_LEVEL", os.Getenv("LOG_LEVEL"))).String())
	log.Info("ready", "detail", "listening, waiting for the claim")

	select {
	case err := <-errc:
		log.Info("shutdown", "reason", "server stopped")
		return err
	case <-ctx.Done():
		log.Info("shutdown", "reason", "signal")
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return nil
}

// Command git-proxy forwards Git smart HTTP from session pods to the provider hosts (ADR 0023).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitproxy"
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
	telemetry.InitLogger("eduide-git-proxy")
	cfgPath := flag.String("config", "/etc/eduide/config.yaml", "path of the deployment config")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		slog.Error("git-proxy failed", "error", err.Error())
		os.Exit(1)
	}
}

// servers returns the Git listener and the one for probes and metrics. Neither has a ReadTimeout or a
// WriteTimeout: a clone or push of gigabytes must not be cut off. Slow headers and idle connections are bounded.
func servers(git, ops http.Handler) []*http.Server {
	return []*http.Server{
		{Addr: ":8080", Handler: git, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10},
		{Addr: ":9090", Handler: ops, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10},
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	g := cfg.GitAccounts
	if len(g.Providers) == 0 || g.InternalURL == "" {
		return errors.New("git-proxy needs gitAccounts.providers and gitAccounts.internalURL")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	az := &gitproxy.ServiceClient{URL: g.InternalURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
	var cas []string
	for _, p := range g.Providers {
		cas = append(cas, p.CAFile)
	}
	pool, err := gitaccount.RootCAs(cas...)
	if err != nil {
		return err
	}
	handler := gitproxy.New(g.Providers, "https://"+cfg.Hosts.Service, az, gitproxy.Transport(pool), slog.Default())

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(telemetry.GitProxyMetrics...)
	ops := http.NewServeMux()
	ops.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	ops.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ops.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srvs := servers(handler, ops)
	slog.Info("starting", "version", logx.Version(), "deployment", cfg.Deployment, "git_addr", srvs[0].Addr, "ops_addr", srvs[1].Addr,
		"providers", len(g.Providers), "internal_url", g.InternalURL, "log_level", logx.Level().String())
	errc := make(chan error, len(srvs))
	for _, s := range srvs {
		go func() { errc <- s.ListenAndServe() }()
	}
	slog.Info("ready", "detail", "serving")
	select {
	case err := <-errc:
		slog.Info("shutdown", "reason", "server stopped")
		return err
	case <-ctx.Done():
		slog.Info("shutdown", "reason", "signal")
	}
	// A running clone gets time to finish.
	sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, s := range srvs {
		_ = s.Shutdown(sctx)
	}
	return nil
}

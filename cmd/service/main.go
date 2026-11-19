// Command service serves the REST API of the control plane (/api/v1) and its metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/httpapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
	telemetry.InitLogger("eduide-service")
	if err := run(); err != nil {
		slog.Error("service failed", "error", err.Error())
		os.Exit(1)
	}
}

// httpServers returns the API server and the server for probes and metrics. Neither has a WriteTimeout or a
// ReadTimeout: SSE streams stay open, and the handlers bound their own body reads and stream writes. Idle
// keep-alive connections and oversized headers are bounded here.
func httpServers(ctx context.Context, api, ops http.Handler) []*http.Server {
	return []*http.Server{
		{Addr: ":8080", Handler: api, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10,
			BaseContext: func(net.Listener) context.Context { return ctx }}, // ends SSE streams on shutdown
		{Addr: ":9090", Handler: ops, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10},
	}
}

func run() error {
	configPath := flag.String("config", "/etc/eduide/config.yaml", "path of the deployment config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, "eduide-service", cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	telemetry.BridgeLogs()

	scheme := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		return err
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	managed := labels.SelectorFromSet(labels.Set{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue})
	nsOnly := cache.ByObject{} // templates and pools come from the chart, which sets its own managed-by value
	c, err := cache.New(restCfg, cache.Options{
		Scheme:            scheme,
		DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.IDESession{}:   {Label: managed},
			&v1alpha1.IDEWorkspace{}: {Label: managed},
			&v1alpha1.IDETemplate{}:  nsOnly,
			&v1alpha1.IDEPool{}:      nsOnly,
		},
	})
	if err != nil {
		return err
	}
	// Reads of the API go to the cache directly; the client only writes.
	writer, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	hub := httpapi.NewHub()
	if err := hub.Register(ctx, c); err != nil {
		return err
	}
	if cfg.OIDC.Admin != nil { // the events informer needs list and watch on events only for the admin API
		if err := hub.RegisterEvents(ctx, c); err != nil {
			return err
		}
	}
	if err := errors.Join(
		func() error { _, err := c.GetInformer(ctx, &v1alpha1.IDETemplate{}); return err }(),
		func() error { _, err := c.GetInformer(ctx, &v1alpha1.IDEPool{}); return err }(),
	); err != nil {
		return err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			slog.Error("cache stopped", "error", err.Error())
			stop()
		}
	}()
	var synced atomic.Bool
	go func() {
		if synced.Store(c.WaitForCacheSync(ctx)); synced.Load() {
			slog.Info("ready", "detail", "caches synced")
		}
	}()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(telemetry.ServiceMetrics...)
	ops := http.NewServeMux()
	ops.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	ops.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ops.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !synced.Load() {
			http.Error(w, "cache not synced", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	var salt []byte
	if cfg.Telemetry.UserSaltFile != "" {
		if salt, err = os.ReadFile(cfg.Telemetry.UserSaltFile); err != nil {
			return err
		}
	}
	if cfg.OIDC.Admin != nil && len(salt) == 0 {
		return errors.New("the admin API needs a non-empty telemetry.userSaltFile")
	}
	api := httpapi.NewServer(cfg, c, writer, hub, salt)
	go api.RunSignInCheck(ctx)
	servers := httpServers(ctx, api.Handler(auth.NewOIDC(cfg.OIDC)), ops)
	logStarting(cfg, servers[0].Addr, servers[1].Addr, salt)
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() { errc <- s.ListenAndServe() }()
	}
	select {
	case err := <-errc:
		slog.Info("shutdown", "reason", "server stopped")
		return err
	case <-ctx.Done():
		slog.Info("shutdown", "reason", "signal")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return nil
}

// logStarting logs the non-secret effective settings.
func logStarting(cfg *config.Config, apiAddr, opsAddr string, salt []byte) {
	slog.Info("starting", "version", logx.Version(), "deployment", cfg.Deployment, "namespace", cfg.Namespace,
		"api_addr", apiAddr, "ops_addr", opsAddr, "service_host", cfg.Hosts.Service, "instance_host", cfg.Hosts.Instance,
		"oidc_issuer", cfg.OIDC.IssuerURL, "otlp_endpoint_set", cfg.Telemetry.OTLPEndpoint != "", "user_salt_set", len(salt) > 0,
		"sessions_per_user", cfg.SessionsPerUser, "launches_per_minute", cfg.RateLimit.LaunchesPerMinute, "log_level", logx.Level().String())
}

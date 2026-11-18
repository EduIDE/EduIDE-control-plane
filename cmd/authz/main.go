// Command authz is the Envoy ext_authz server (ADR 0005).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/authz"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
	cfgPath := flag.String("config", "/etc/eduide/config.yaml", "path of the config file")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		slog.Error("authz failed", "error", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTel, err := telemetry.Setup(ctx, "eduide-authz", cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTel(context.Background()) }()
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	var salt []byte
	if cfg.Telemetry.UserSaltFile != "" {
		if salt, err = os.ReadFile(cfg.Telemetry.UserSaltFile); err != nil {
			return err
		}
	} else {
		slog.Warn("telemetry.userSaltFile is unset: user ids in logs are unsalted hashes")
	}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	sel := labels.SelectorFromSet(labels.Set{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue})
	c, err := cache.New(restCfg, cache.Options{
		Scheme:            scheme,
		DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}},
		ByObject:          map[client.Object]cache.ByObject{&v1alpha1.IDESession{}: {Label: sel}},
	})
	if err != nil {
		return err
	}
	if err := authz.IndexSessions(ctx, c); err != nil {
		return err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			slog.Error("cache stopped", "error", err)
			stop()
		}
	}()

	srv := &authz.Server{
		Sessions:      authz.CacheSessions{Cache: c, Namespace: cfg.Namespace},
		UserClaim:     cfg.OIDC.UserClaim,
		IDTokenCookie: cfg.OIDC.IDTokenCookie,
		InstanceHost:  cfg.Hosts.Instance,
		WebviewHost:   cfg.Hosts.Webview,
		Salt:          salt,
	}
	go discover(ctx, cfg, srv)

	reg := prometheus.NewRegistry()
	reg.MustRegister(telemetry.AuthzMetrics...)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ready := func(ctx context.Context) bool { return srv.Ready() && c.WaitForCacheSync(ctx) }
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready(r.Context()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	httpSrv := &http.Server{Addr: ":9090", Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", ":9001") //nolint:gosec // the pod serves the gateway
	if err != nil {
		return err
	}
	g := grpc.NewServer()
	authv3.RegisterAuthorizationServer(g, srv)
	h := health.NewServer()
	healthpb.RegisterHealthServer(g, h)
	go serveWhenReady(ctx, h, ready, time.Second)

	errc := make(chan error, 2)
	go func() { errc <- g.Serve(lis) }()
	go func() { errc <- httpSrv.ListenAndServe() }()
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	h.Shutdown()
	done := make(chan struct{})
	go func() { g.GracefulStop(); close(done) }()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-sctx.Done():
		g.Stop()
	}
	_ = httpSrv.Shutdown(sctx)
	return err
}

// discover retries OIDC discovery until it works, then arms the verifier.
func discover(ctx context.Context, cfg *config.Config, srv *authz.Server) {
	// Without a timeout one hung discovery or JWKS fetch blocks forever.
	client := &http.Client{Timeout: 10 * time.Second}
	ctx = oidc.ClientContext(ctx, client)
	for {
		p, err := oidc.NewProvider(ctx, cfg.OIDC.IssuerURL)
		if err == nil {
			var v *oidc.IDTokenVerifier
			if v, err = authz.Verifier(p, cfg.OIDC.GatewayClientID, client, 30*time.Second); err == nil {
				srv.SetVerifier(v)
				return
			}
		}
		slog.Warn("oidc discovery failed, retrying", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// serveWhenReady keeps the gRPC health status NOT_SERVING until ready reports true.
func serveWhenReady(ctx context.Context, h *health.Server, ready func(context.Context) bool, every time.Duration) {
	h.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	for !ready(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
}

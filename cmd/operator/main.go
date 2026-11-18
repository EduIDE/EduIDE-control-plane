// Command operator runs the EduIDE controllers: template, pool, session and workspace.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/controller"
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
	telemetry.InitLogger("eduide-operator")
	cfgPath := flag.String("config", "/etc/eduide/config.yaml", "path of the deployment config")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		slog.Error("operator failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	ctx := ctrl.SetupSignalHandler()
	shutdown, err := telemetry.Setup(ctx, "eduide-operator", cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()
	telemetry.BridgeLogs()
	var salt []byte
	if cfg.Telemetry.UserSaltFile != "" {
		if salt, err = os.ReadFile(cfg.Telemetry.UserSaltFile); err != nil {
			return err
		}
	}
	metrics.Registry.MustRegister(telemetry.OperatorMetrics...)

	restCfg := ctrl.GetConfigOrDie()
	dc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return err
	}
	if err := controller.CheckCRDs(dc); err != nil {
		return err
	}
	managed := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{v1.LabelManagedBy: v1.LabelManagedByValue})}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                  controller.NewScheme(),
		Metrics:                 metricsserver.Options{BindAddress: ":9464"},
		HealthProbeBindAddress:  ":8081",
		LeaderElection:          true,
		LeaderElectionID:        "eduide-operator",
		LeaderElectionNamespace: cfg.Namespace,
		// The process exits right after the manager stops, so the next replica takes over at once on a rollout.
		LeaderElectionReleaseOnCancel: true,
		Cache: cache.Options{
			// Managed fields are a large part of every pod and are never read.
			DefaultTransform:  cache.TransformStripManagedFields(),
			DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}:                   managed,
				&corev1.Service{}:               managed,
				&corev1.Secret{}:                managed,
				&corev1.PersistentVolumeClaim{}: managed,
				&gatewayv1.HTTPRoute{}:          managed,
				&appsv1.DaemonSet{}:             managed,
			},
		},
	})
	if err != nil {
		return err
	}
	if err := controller.SetupWithManager(mgr, controller.Options{Config: cfg, Salt: salt}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	logStarting(cfg, salt)
	// Ready once this replica leads and the controllers run; a standby replica logs nothing more.
	go func() {
		select {
		case <-mgr.Elected():
			slog.Info("ready", "detail", "elected leader, controllers running")
		case <-ctx.Done():
		}
	}()
	err = mgr.Start(ctx)
	reason := "signal"
	if err != nil {
		reason = "manager stopped"
	}
	slog.Info("shutdown", "reason", reason)
	return err
}

// logStarting logs the non-secret effective settings.
func logStarting(cfg *config.Config, salt []byte) {
	slog.Info("starting", "version", logx.Version(), "deployment", cfg.Deployment, "namespace", cfg.Namespace,
		"metrics_addr", ":9464", "probe_addr", ":8081", "otlp_endpoint_set", cfg.Telemetry.OTLPEndpoint != "",
		"user_salt_set", len(salt) > 0, "log_level", logx.Level().String())
}

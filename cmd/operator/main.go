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
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
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
	if err := controller.SetupWithManager(mgr, controller.Options{Config: cfg}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	slog.Info("operator starting", "namespace", cfg.Namespace, "deployment", cfg.Deployment)
	return mgr.Start(ctx)
}

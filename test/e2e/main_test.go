//go:build e2e

// Package e2e runs against the kind cluster that run.sh builds. It never touches another cluster.
package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	serviceHost  = "ide.e2e.test"
	instanceHost = "instance.ide.e2e.test"
	keycloakHost = "keycloak.e2e.test"
	smokeHost    = "smoke.e2e.test"
	issuer       = "https://keycloak.e2e.test/realms/eduide"
	apiBase      = "https://ide.e2e.test/api/v1"
)

var (
	kube      client.Client
	clientset kubernetes.Interface
	namespace = envOr("E2E_NAMESPACE", "eduide-e2e")
	hostPort  = envOr("E2E_PORT", "18443")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// setup refuses to run unless KUBECONFIG is set and points to a server on 127.0.0.1:
// the default kube context of a developer machine can be a real cluster.
func setup() error {
	path := os.Getenv("KUBECONFIG")
	if path == "" || strings.Contains(path, string(os.PathListSeparator)) {
		return fmt.Errorf("KUBECONFIG must name exactly one file (run test/e2e/run.sh), got %q", path)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return err
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Hostname() != "127.0.0.1" {
		return fmt.Errorf("refusing to run: API server %q is not on 127.0.0.1", cfg.Host)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	if kube, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return err
	}
	clientset, err = kubernetes.NewForConfig(cfg)
	return err
}

// logsOf returns the logs of all containers of a pod.
func logsOf(ctx context.Context, pod *corev1.Pod) (string, error) {
	var sb strings.Builder
	for _, c := range pod.Spec.Containers {
		b, err := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: c.Name}).DoRaw(ctx)
		if err != nil {
			return "", err
		}
		sb.Write(b)
	}
	return sb.String(), nil
}

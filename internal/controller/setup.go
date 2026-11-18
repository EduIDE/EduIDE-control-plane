package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/recorder"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentclient"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const fieldManager = "eduide-operator"

// indexWorkspaceRef indexes sessions by the workspace they use.
const indexWorkspaceRef = "spec.workspaceRef"

// Clock is the time source; tests move it.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Options configure the controllers.
type Options struct {
	Config *config.Config
	// Clock defaults to the wall clock.
	Clock Clock
	// Agent defaults to agentclient.New().
	Agent *agentclient.Client
	// AgentURL resolves the control address of a pod's agent. Default: http://<podIP>:8081.
	AgentURL func(*corev1.Pod) (string, error)
	// Salt keys the pseudonymous user id in logs (telemetry.userSaltFile).
	Salt []byte
	// Registry reads the template metadata from image manifests (U12). Default: anonymous pulls.
	Registry ImageRegistry
	// PollInterval between agent status polls of a session. Default 60s.
	PollInterval time.Duration
}

// deps are shared by all reconcilers.
type deps struct {
	client.Client
	reader   client.Reader
	scheme   *runtime.Scheme
	cfg      *config.Config
	clock    Clock
	pressure *pressure
	agent    *agentclient.Client
	agentURL func(*corev1.Pod) (string, error)
	poll     time.Duration
	salt     []byte
	events   recorder.EventRecorder
	registry ImageRegistry
	images   *imageCache
}

// NewScheme returns a scheme with core Kubernetes, Gateway API and EduIDE types.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = gatewayv1.Install(s)
	_ = v1.AddToScheme(s)
	return s
}

// SetupWithManager registers all controllers and the gauge updater.
func SetupWithManager(mgr ctrl.Manager, o Options) error {
	d := &deps{
		Client: mgr.GetClient(), reader: mgr.GetAPIReader(), scheme: mgr.GetScheme(),
		cfg: o.Config, clock: o.Clock, agent: o.Agent, agentURL: o.AgentURL, poll: o.PollInterval,
		salt: o.Salt, events: mgr.GetEventRecorder(fieldManager),
		registry: o.Registry, images: &imageCache{},
	}
	if d.registry == nil {
		d.registry = remoteRegistry{}
	}
	if d.clock == nil {
		d.clock = realClock{}
	}
	d.pressure = newPressure(d.clock)
	if d.agent == nil {
		d.agent = agentclient.New()
	}
	if d.agentURL == nil {
		d.agentURL = func(p *corev1.Pod) (string, error) {
			if p.Status.PodIP == "" {
				return "", fmt.Errorf("pod %s has no IP", p.Name)
			}
			return "http://" + p.Status.PodIP + ":8081", nil
		}
	}
	if d.poll == 0 {
		d.poll = time.Minute
	}
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1.IDESession{}, indexWorkspaceRef, func(o client.Object) []string {
		if ref := o.(*v1.IDESession).Spec.WorkspaceRef; ref != "" {
			return []string{ref}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := (&templateReconciler{deps: d}).setup(mgr); err != nil {
		return err
	}
	if err := (&poolReconciler{deps: d}).setup(mgr); err != nil {
		return err
	}
	if err := (&sessionReconciler{deps: d}).setup(mgr); err != nil {
		return err
	}
	if err := (&workspaceReconciler{deps: d}).setup(mgr); err != nil {
		return err
	}
	return mgr.Add(manager.RunnableFunc(d.runGauges))
}

func opts(n int) controller.Options { return controller.Options{MaxConcurrentReconciles: n} }

// pressure remembers which node pools cannot place a session pod right now (G8).
// Sessions that wait for a pod renew the mark, so it lapses on its own.
type pressure struct {
	mu    sync.Mutex
	until map[string]time.Time
	clock Clock
}

const pressureTTL = 15 * time.Second

func newPressure(c Clock) *pressure { return &pressure{until: map[string]time.Time{}, clock: c} }

func gaugeName(nodePool string) string {
	if nodePool == "" {
		return "default"
	}
	return nodePool
}

func (p *pressure) Mark(nodePool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.until[nodePool] = p.clock.Now().Add(pressureTTL)
	telemetry.CapacityPressure.WithLabelValues(gaugeName(nodePool)).Set(1)
}

func (p *pressure) Active(nodePool string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	u, ok := p.until[nodePool]
	if ok && !p.clock.Now().Before(u) {
		delete(p.until, nodePool)
		telemetry.CapacityPressure.WithLabelValues(gaugeName(nodePool)).Set(0)
		return false
	}
	return ok
}

// runGauges keeps eduide_sessions and eduide_workspaces current from the cache.
func (d *deps) runGauges(ctx context.Context) error {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		var ss v1.IDESessionList
		var ws v1.IDEWorkspaceList
		if d.List(ctx, &ss, client.InNamespace(d.cfg.Namespace)) == nil && d.List(ctx, &ws, client.InNamespace(d.cfg.Namespace)) == nil {
			telemetry.Sessions.Reset()
			telemetry.Workspaces.Reset()
			for _, s := range ss.Items {
				ph := string(s.Status.Phase)
				if ph == "" {
					ph = string(v1.SessionPending)
				}
				telemetry.Sessions.WithLabelValues(s.Spec.TemplateRef, ph).Inc()
			}
			for _, w := range ws.Items {
				ph := string(w.Status.Phase)
				if ph == "" {
					ph = string(v1.WorkspacePending)
				}
				telemetry.Workspaces.WithLabelValues(ph).Inc()
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

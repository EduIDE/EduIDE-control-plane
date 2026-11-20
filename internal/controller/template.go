package controller

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const componentPrepull = "prepull"

type templateReconciler struct{ *deps }

func (r *templateReconciler) setup(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("template").For(&v1.IDETemplate{}).Owns(&appsv1.DaemonSet{}).
		WithOptions(opts(4)).Complete(r)
}

// validateTemplate checks what the CRD schema and CEL rules cannot.
func validateTemplate(t *v1.IDETemplate) error {
	for _, k := range t.Spec.InjectableKeys {
		if k.Format == v1.FormatPattern {
			if _, err := regexp.Compile(k.Pattern); err != nil {
				return fmt.Errorf("injectable key %s: invalid pattern: %w", k.Name, err)
			}
		}
	}
	seen := map[string]bool{}
	for _, e := range t.Spec.Env {
		if seen[e.Name] {
			return fmt.Errorf("duplicate env %s", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}

func (r *templateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	t := &v1.IDETemplate{}
	if err := r.Get(ctx, req.NamespacedName, t); err != nil {
		if apierrors.IsNotFound(err) {
			telemetry.TemplateResources.DeletePartialMatch(prometheus.Labels{"template": req.Name})
			telemetry.TemplateTimeout.DeletePartialMatch(prometheus.Labels{"template": req.Name})
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	recordResources(t)
	orig := t.DeepCopy()
	cond := metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Valid", ObservedGeneration: t.Generation}
	verr := validateTemplate(t)
	if verr != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Invalid", verr.Error()
	}
	meta.SetStatusCondition(&t.Status.Conditions, cond)
	t.Status.ObservedGeneration = t.Generation
	var recheck time.Duration
	if verr == nil {
		recheck = r.reconcileImage(ctx, t)
	}
	if err := r.patchStatus(ctx, orig, t); err != nil {
		return ctrl.Result{}, err
	}
	if verr != nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: recheck}, r.reconcilePrepull(ctx, t)
}

// recordResources exports the IDE container resources of a template; only defined values get a series.
func recordResources(t *v1.IDETemplate) {
	idle, warning, max := t.ResolvedTimeouts()
	for kind, d := range map[string]time.Duration{"idle": idle, "warning": warning, "max": max} {
		telemetry.TemplateTimeout.WithLabelValues(t.Name, kind).Set(d.Seconds())
	}
	for kind, list := range map[string]corev1.ResourceList{"request": t.Spec.Resources.Requests, "limit": t.Spec.Resources.Limits} {
		for res, name := range map[string]corev1.ResourceName{"cpu": corev1.ResourceCPU, "memory": corev1.ResourceMemory} {
			// Only a value that is gone loses its series, so a scrape never sees a gap for unchanged ones.
			if q, ok := list[name]; ok {
				telemetry.TemplateResources.WithLabelValues(t.Name, res, kind).Set(q.AsApproximateFloat64())
			} else {
				telemetry.TemplateResources.DeleteLabelValues(t.Name, res, kind)
			}
		}
	}
}

func prepullName(t string) string { return "prepull-" + t }

// reconcilePrepull keeps one DaemonSet per template that pulls the image on the nodes of its placement.
func (r *templateReconciler) reconcilePrepull(ctx context.Context, t *v1.IDETemplate) error {
	key := client.ObjectKey{Namespace: t.Namespace, Name: prepullName(t.Name)}
	if !r.cfg.Placement.Prepull.Enabled {
		ds := &appsv1.DaemonSet{}
		if err := r.Get(ctx, key, ds); err != nil {
			return client.IgnoreNotFound(err)
		}
		return client.IgnoreNotFound(r.Delete(ctx, ds))
	}
	pl := r.placement(t)
	labels := map[string]string{v1.LabelManagedBy: v1.LabelManagedByValue, v1.LabelComponent: componentPrepull, v1.LabelTemplate: t.Name}
	no := false
	grace := int64(0)
	tiny := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m"), corev1.ResourceMemory: resource.MustParse("8Mi")}
	ds := &appsv1.DaemonSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeSelector: pl.NodeSelector, Tolerations: pl.Tolerations, Affinity: pl.Affinity,
					AutomountServiceAccountToken: &no, EnableServiceLinks: &no, TerminationGracePeriodSeconds: &grace,
					InitContainers: []corev1.Container{{
						Name: "pull", Image: t.Spec.Image, ImagePullPolicy: t.Spec.ImagePullPolicy,
						Command: []string{"sh", "-c", "true"},
					}, {
						// The agent image is pulled too, otherwise a cold start waits for it after the IDE image.
						Name: "pull-agent", Image: r.cfg.Agent.Image, Args: []string{"noop"},
					}},
					Containers: []corev1.Container{{
						Name: "pause", Image: r.cfg.Placement.Prepull.PauseImage,
						Resources: corev1.ResourceRequirements{Requests: tiny, Limits: tiny},
					}},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(t, ds, r.scheme); err != nil {
		return err
	}
	err := r.Patch(ctx, ds, client.Apply, //nolint:staticcheck // typed apply; the replacement needs generated apply configurations
		client.FieldOwner(fieldManager), client.ForceOwnership)
	if apierrors.IsInvalid(err) {
		return fmt.Errorf("prepull daemonset: %w", err)
	}
	return err
}

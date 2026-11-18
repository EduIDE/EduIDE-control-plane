package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/claim"
)

const (
	agentUID       = 65532
	tokenMountPath = "/var/run/eduide" //nolint:gosec // a directory, not a credential
	defaultMount   = "/home/project"
)

func podName(instance string) string   { return "ide-" + instance }
func tokenName(instance string) string { return "ide-" + instance + "-token" }

// placement is the effective placement: a template field overrides the deployment profile.
func (d *deps) placement(t *v1.IDETemplate) v1.Placement {
	p := v1.Placement{NodeSelector: d.cfg.Placement.NodeSelector, Tolerations: d.cfg.Placement.Tolerations, Affinity: d.cfg.Placement.Affinity}
	if len(t.Spec.Placement.NodeSelector) > 0 {
		p.NodeSelector = t.Spec.Placement.NodeSelector
	}
	if len(t.Spec.Placement.Tolerations) > 0 {
		p.Tolerations = t.Spec.Placement.Tolerations
	}
	if t.Spec.Placement.Affinity != nil {
		p.Affinity = t.Spec.Placement.Affinity
	}
	return p
}

// nodePool is the node pool name of the template's placement, "" when none is configured.
func (d *deps) nodePool(t *v1.IDETemplate) string {
	if d.cfg.Placement.NodePoolLabel == "" {
		return ""
	}
	return d.placement(t).NodeSelector[d.cfg.Placement.NodePoolLabel]
}

func instanceLabels(template, instance string, extra map[string]string) map[string]string {
	l := map[string]string{
		v1.LabelManagedBy: v1.LabelManagedByValue,
		v1.LabelComponent: v1.ComponentSession,
		v1.LabelTemplate:  template,
		v1.LabelInstance:  instance,
	}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

// buildPod renders a session pod. claimName is the workspace PVC, empty for an emptyDir.
func (d *deps) buildPod(t *v1.IDETemplate, instance string, extra map[string]string, claimName string) *corev1.Pod {
	port := t.Spec.Port
	if port == 0 {
		port = 3000
	}
	mount := t.Spec.Persistence.MountPath
	if mount == "" {
		mount = defaultMount
	}
	labels := instanceLabels(t.Name, instance, extra)
	pl := d.placement(t)
	if np := d.nodePool(t); np != "" {
		labels[v1.LabelNodePool] = np
	}
	env := make([]corev1.EnvVar, 0, len(t.Spec.Env))
	for _, e := range t.Spec.Env {
		env = append(env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	ide := corev1.Container{
		Name: "ide", Image: t.Spec.Image, ImagePullPolicy: t.Spec.ImagePullPolicy, Env: env,
		Resources:    t.Spec.Resources,
		VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: mount}},
	}
	podSec := &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	yes, no := true, false
	ide.SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	if u := t.Spec.RunAsUser; u != nil {
		ide.SecurityContext.RunAsUser, ide.SecurityContext.RunAsNonRoot = u, new(*u != 0)
		podSec.FSGroup = u
	}
	agent := corev1.Container{
		Name: "agent", Image: d.cfg.Agent.Image, Resources: d.cfg.Agent.Resources,
		Ports: []corev1.ContainerPort{{Name: "proxy", ContainerPort: 8080}, {Name: "control", ContainerPort: 8081}},
		Env: []corev1.EnvVar{
			{Name: "AGENT_IDE_PORT", Value: strconv.Itoa(int(port))},
			{Name: "AGENT_TOKEN_FILE", Value: tokenMountPath + "/" + v1.InstanceTokenKey},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "token", MountPath: tokenMountPath, ReadOnly: true}},
		ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, FailureThreshold: 3, ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(8081)}}},
		LivenessProbe: &corev1.Probe{PeriodSeconds: 10, FailureThreshold: 3, InitialDelaySeconds: 5, ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8081)}}},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &yes, RunAsUser: new(int64(agentUID)), AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes,
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	ws := corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	if claimName != "" {
		ws.VolumeSource = corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}}
	}
	mode := int32(0o444)
	tok := corev1.Volume{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: tokenName(instance), DefaultMode: &mode,
		Items: []corev1.KeyToPath{{Key: v1.InstanceTokenKey, Path: v1.InstanceTokenKey}},
	}}}
	ann := map[string]string{v1.AnnotationSpecHash: d.specHash(t)}
	if b := t.Spec.Bandwidth; b != nil {
		if b.Ingress != nil {
			ann["kubernetes.io/ingress-bandwidth"] = b.Ingress.String()
		}
		if b.Egress != nil {
			ann["kubernetes.io/egress-bandwidth"] = b.Egress.String()
		}
	}
	grace := int64(10)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName(instance), Namespace: d.cfg.Namespace, Labels: labels, Annotations: ann},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no, EnableServiceLinks: &no, SecurityContext: podSec,
			TerminationGracePeriodSeconds: &grace,
			NodeSelector:                  pl.NodeSelector, Tolerations: pl.Tolerations, Affinity: pl.Affinity,
			Containers: []corev1.Container{ide, agent},
			Volumes:    []corev1.Volume{ws, tok},
		},
	}
}

// specHash identifies what a pod is built from: the template spec, the agent image and the deployment placement.
// A pool replaces idle pods with another hash.
func (d *deps) specHash(t *v1.IDETemplate) string {
	b, _ := json.Marshal([]any{t.Spec, d.cfg.Agent.Image, d.cfg.Placement})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// repairChildren makes sure token Secret, Service and HTTPRoute of an existing instance exist. gone is true when
// the token Secret is missing and the token cannot come back: the pod has read it already or its route carries it.
// The caller discards such an instance. A pod that has not started yet, after a create that failed half way, gets a new token.
func (d *deps) repairChildren(ctx context.Context, pod *corev1.Pod) (gone bool, err error) {
	instance := pod.Labels[v1.LabelInstance]
	key := client.ObjectKey{Namespace: pod.Namespace, Name: tokenName(instance)}
	if err := d.Get(ctx, key, &corev1.Secret{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, err
		}
		// The cache can lag behind a Secret created a moment ago.
		if err := d.reader.Get(ctx, key, &corev1.Secret{}); err != nil {
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			routeKey := client.ObjectKey{Namespace: pod.Namespace, Name: podName(instance)}
			if claim.Started(pod) || d.Get(ctx, routeKey, &gatewayv1.HTTPRoute{}) == nil {
				return true, nil
			}
		}
	}
	return false, d.ensureChildren(ctx, pod)
}

// ensureChildren creates token Secret, Service and HTTPRoute of an instance, owned by the pod.
// Existing objects stay as they are. The route is created last, so its presence marks the set complete.
func (d *deps) ensureChildren(ctx context.Context, pod *corev1.Pod) error {
	instance := pod.Labels[v1.LabelInstance]
	route := &gatewayv1.HTTPRoute{}
	key := client.ObjectKey{Namespace: pod.Namespace, Name: podName(instance)}
	if d.Get(ctx, key, route) == nil && d.Get(ctx, key, &corev1.Service{}) == nil {
		return nil
	}
	own := func(o client.Object) error { return controllerutil.SetOwnerReference(pod, o, d.scheme) }
	meta := func(name string, extra map[string]string) metav1.ObjectMeta {
		l := map[string]string{v1.LabelManagedBy: v1.LabelManagedByValue, v1.LabelComponent: v1.ComponentSession, v1.LabelInstance: instance}
		for k, v := range extra {
			l[k] = v
		}
		return metav1.ObjectMeta{Name: name, Namespace: pod.Namespace, Labels: l}
	}

	token, err := d.ensureToken(ctx, pod, meta(tokenName(instance), nil), own)
	if err != nil {
		return err
	}
	svc := &corev1.Service{ObjectMeta: meta(podName(instance), nil), Spec: corev1.ServiceSpec{
		Selector: map[string]string{v1.LabelInstance: instance},
		Ports:    []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromString("proxy")}},
	}}
	if err := own(svc); err != nil {
		return err
	}
	if err := d.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	r := d.buildRoute(meta(podName(instance), map[string]string{v1.LabelRoute: v1.RouteSession}), instance, token)
	if err := own(r); err != nil {
		return err
	}
	if err := d.Create(ctx, r); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (d *deps) ensureToken(ctx context.Context, pod *corev1.Pod, meta metav1.ObjectMeta, own func(client.Object) error) (string, error) {
	key := client.ObjectKey{Namespace: meta.Namespace, Name: meta.Name}
	sec := &corev1.Secret{}
	err := d.Get(ctx, key, sec)
	if apierrors.IsNotFound(err) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		sec = &corev1.Secret{ObjectMeta: meta, Data: map[string][]byte{v1.InstanceTokenKey: []byte(hex.EncodeToString(b))}}
		if err := own(sec); err != nil {
			return "", err
		}
		err = d.Create(ctx, sec)
		if apierrors.IsAlreadyExists(err) {
			err = d.reader.Get(ctx, key, sec)
		}
	}
	if err != nil {
		return "", err
	}
	if len(sec.Data[v1.InstanceTokenKey]) == 0 {
		return "", fmt.Errorf("secret %s has no token", meta.Name)
	}
	return string(sec.Data[v1.InstanceTokenKey]), nil
}

func (d *deps) buildRoute(meta metav1.ObjectMeta, instance, token string) *gatewayv1.HTTPRoute {
	g := d.cfg.Gateway
	ns := gatewayv1.Namespace(g.Namespace)
	parent := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(g.Name), Namespace: &ns}
	if g.SectionName != "" {
		sn := gatewayv1.SectionName(g.SectionName)
		parent.SectionName = &sn
	}
	if g.Port != 0 {
		gp := g.Port
		parent.Port = &gp
	}
	prefix, root, port := "/"+instance+"/", "/", gatewayv1.PortNumber(8080)
	return &gatewayv1.HTTPRoute{ObjectMeta: meta, Spec: gatewayv1.HTTPRouteSpec{
		CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
		Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(d.cfg.Hosts.Instance), gatewayv1.Hostname(d.cfg.Hosts.Webview)},
		Rules: []gatewayv1.HTTPRouteRule{{
			Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Type: new(gatewayv1.PathMatchPathPrefix), Value: &prefix}}},
			Filters: []gatewayv1.HTTPRouteFilter{
				{Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier, RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
					Set: []gatewayv1.HTTPHeader{{Name: v1.InstanceTokenHeader, Value: token}}}},
				{Type: gatewayv1.HTTPRouteFilterURLRewrite, URLRewrite: &gatewayv1.HTTPURLRewriteFilter{
					Path: &gatewayv1.HTTPPathModifier{Type: gatewayv1.PrefixMatchHTTPPathModifier, ReplacePrefixMatch: &root}}},
				// Only HSTS: the IDE sets its own CSP and frame options.
				{Type: gatewayv1.HTTPRouteFilterResponseHeaderModifier, ResponseHeaderModifier: &gatewayv1.HTTPHeaderFilter{
					Set: []gatewayv1.HTTPHeader{{Name: "Strict-Transport-Security", Value: "max-age=31536000"}}}},
			},
			BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(podName(instance)), Port: &port}}}},
		}},
	}}
}

// createInstance creates a pod with its children. The owner becomes the controller of the pod.
func (d *deps) createInstance(ctx context.Context, t *v1.IDETemplate, owner client.Object, extra map[string]string, claimName string) (*corev1.Pod, error) {
	pod := d.buildPod(t, claim.NewInstanceID(), extra, claimName)
	if err := controllerutil.SetControllerReference(owner, pod, d.scheme); err != nil {
		return nil, err
	}
	if err := d.Create(ctx, pod); err != nil {
		return nil, err
	}
	return pod, d.ensureChildren(ctx, pod)
}

// patchStatus writes status when it differs from orig.
func (d *deps) patchStatus(ctx context.Context, orig, obj client.Object) error {
	p := client.MergeFrom(orig)
	if b, err := p.Data(obj); err != nil || string(b) == "{}" {
		return err
	}
	return d.Status().Patch(ctx, obj, p, client.FieldOwner(fieldManager))
}

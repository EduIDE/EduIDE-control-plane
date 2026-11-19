package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

type workspaceReconciler struct{ *deps }

func (r *workspaceReconciler) setup(mgr ctrl.Manager) error {
	bySession := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if ref := o.(*v1.IDESession).Spec.WorkspaceRef; ref != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: ref}}}
		}
		return nil
	})
	return ctrl.NewControllerManagedBy(mgr).Named("workspace").For(&v1.IDEWorkspace{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&v1.IDESession{}, bySession).
		WithOptions(opts(4)).Complete(r)
}

func (r *workspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ws := &v1.IDEWorkspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil || ws.DeletionTimestamp != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := ws.DeepCopy()
	now := r.clock.Now()
	ready := func(status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{Type: v1.ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: ws.Generation})
	}
	pending := func(reason, msg string) (ctrl.Result, error) {
		ws.Status.Phase = v1.WorkspacePending
		ready(metav1.ConditionFalse, reason, msg)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.patchStatus(ctx, orig, ws)
	}

	tmpl := &v1.IDETemplate{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: ws.Spec.TemplateRef}, tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return pending("TemplateNotFound", fmt.Sprintf("template %s not found", ws.Spec.TemplateRef))
		}
		return ctrl.Result{}, err
	}

	claimName, msg, err := r.ensureVolume(ctx, ws, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if claimName == "" && ws.Status.ClaimName != "" {
		// The volume held data once. A new empty one would hide the loss, so the workspace waits for a restore (F34).
		if c := meta.FindStatusCondition(ws.Status.Conditions, v1.ConditionReady); c == nil || c.Reason != v1.ReasonVolumeLost {
			slog.WarnContext(ctx, "workspace volume lost", "workspace", ws.Name, "claim", ws.Status.ClaimName, "user", telemetry.UserID(r.salt, ws.Spec.Owner))
			r.events.Eventf(ws, nil, corev1.EventTypeWarning, v1.ReasonVolumeLost, v1.ReasonVolumeLost, "claim %s is gone; delete the workspace or restore the claim", ws.Status.ClaimName)
		}
		ready(metav1.ConditionFalse, v1.ReasonVolumeLost, fmt.Sprintf("claim %s is gone", ws.Status.ClaimName))
		return ctrl.Result{RequeueAfter: time.Minute}, r.patchStatus(ctx, orig, ws)
	}
	if claimName == "" {
		return pending("ClaimNotFound", msg)
	}
	st := &ws.Status
	st.ClaimName, st.ObservedGeneration = claimName, ws.Generation
	if st.LastUsed == nil {
		// New workspaces count from creation, imported ones from the import (G19).
		t := metav1.NewTime(now)
		if ws.Spec.Persistence.ExistingClaimName == "" {
			t = ws.CreationTimestamp
		}
		st.LastUsed = &t
	}
	exp := metav1.NewTime(st.LastUsed.Add(r.retention(ws, tmpl)))
	st.ExpiresAt = &exp
	ready(metav1.ConditionTrue, "VolumeReady", "")

	var sessions v1.IDESessionList
	if err := r.List(ctx, &sessions, client.InNamespace(ws.Namespace), client.MatchingFields{indexWorkspaceRef: ws.Name}); err != nil {
		return ctrl.Result{}, err
	}
	inUse := false
	for _, s := range sessions.Items {
		inUse = inUse || s.Status.Phase != v1.SessionFailed
	}
	retained := ws.Annotations[v1.AnnotationRetain] == "true"
	if debugOn(ctx) {
		slog.DebugContext(ctx, "workspace ttl", "workspace", ws.Name, "last_used", st.LastUsed.Time, "retention", r.retention(ws, tmpl).String(),
			"expires_at", exp.Time, "in_use", inUse, "retained", retained, "expired", !now.Before(exp.Time))
	}

	var res ctrl.Result
	if inUse || retained || now.Before(exp.Time) {
		st.Phase, st.ExpiredAt = v1.WorkspaceReady, nil
		if !inUse && !retained {
			res.RequeueAfter = exp.Sub(now) + time.Second
		}
	} else {
		st.Phase = v1.WorkspaceExpired
		if st.ExpiredAt == nil {
			t := metav1.NewTime(now)
			st.ExpiredAt = &t
			slog.InfoContext(ctx, "workspace expired", "workspace", ws.Name, "user", telemetry.UserID(r.salt, ws.Spec.Owner), "grace", r.cfg.Workspaces.Grace.Duration.String())
		}
		if gone := st.ExpiredAt.Add(r.cfg.Workspaces.Grace.Duration); !now.Before(gone) {
			// The cache can miss a session created a moment ago, so ask the API server before deleting.
			if used, err := r.usedLive(ctx, ws); err != nil || used {
				return ctrl.Result{RequeueAfter: time.Second}, err
			}
			// The PVC follows through its owner reference.
			slog.InfoContext(ctx, "workspace deleted after its grace period", "workspace", ws.Name, "user", telemetry.UserID(r.salt, ws.Spec.Owner))
			return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, ws))
		} else {
			res.RequeueAfter = gone.Sub(now) + time.Second
		}
	}
	return res, r.patchStatus(ctx, orig, ws)
}

// usedLive reports whether a session that has not failed uses the workspace, read from the API server.
func (r *workspaceReconciler) usedLive(ctx context.Context, ws *v1.IDEWorkspace) (bool, error) {
	var l v1.IDESessionList
	if err := r.reader.List(ctx, &l, client.InNamespace(ws.Namespace)); err != nil {
		return false, err
	}
	for _, s := range l.Items {
		if s.Spec.WorkspaceRef == ws.Name && s.Status.Phase != v1.SessionFailed {
			return true, nil
		}
	}
	return false, nil
}

func (r *workspaceReconciler) retention(ws *v1.IDEWorkspace, t *v1.IDETemplate) time.Duration {
	switch {
	case ws.Spec.Retention != nil:
		return ws.Spec.Retention.Duration
	case t.Spec.Retention != nil:
		return t.Spec.Retention.Duration
	}
	return r.cfg.Workspaces.Retention.Duration
}

// ensureVolume creates the PVC or checks the imported one. It returns the claim name, or "" with a reason.
func (r *workspaceReconciler) ensureVolume(ctx context.Context, ws *v1.IDEWorkspace, t *v1.IDETemplate) (string, string, error) {
	if name := ws.Spec.Persistence.ExistingClaimName; name != "" {
		// Imported claims carry no managed-by label, so the cache does not see them.
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.reader.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: name}, pvc); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Sprintf("claim %s not found", name), nil
			}
			return "", "", err
		}
		// Adopt the claim, so the retention deletes it like one the operator created.
		if err := r.adopt(ctx, ws, pvc); err != nil {
			return "", "", err
		}
		return name, "", nil
	}
	key := client.ObjectKey{Namespace: ws.Namespace, Name: ws.Name}
	if err := r.Get(ctx, key, &corev1.PersistentVolumeClaim{}); err == nil {
		return ws.Name, "", nil
	} else if !apierrors.IsNotFound(err) {
		return "", "", err
	}
	if ws.Status.ClaimName != "" {
		return "", fmt.Sprintf("claim %s is gone", ws.Status.ClaimName), nil
	}
	size := r.cfg.Workspaces.DefaultSize
	switch {
	case ws.Spec.Persistence.Size != nil:
		size = ws.Spec.Persistence.Size
	case t.Spec.Persistence.Size != nil:
		size = t.Spec.Persistence.Size
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: ws.Name, Namespace: ws.Namespace, Labels: map[string]string{
			v1.LabelManagedBy: v1.LabelManagedByValue, v1.LabelComponent: v1.ComponentWorkspace, v1.LabelWorkspace: ws.Name, v1.LabelTemplate: t.Name}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *size}},
		},
	}
	switch {
	case ws.Spec.Persistence.StorageClassName != nil:
		pvc.Spec.StorageClassName = ws.Spec.Persistence.StorageClassName
	case r.cfg.Workspaces.StorageClassName != "":
		pvc.Spec.StorageClassName = new(r.cfg.Workspaces.StorageClassName)
	}
	if err := controllerutil.SetControllerReference(ws, pvc, r.scheme); err != nil {
		return "", "", err
	}
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", "", err
	}
	return ws.Name, "", nil
}

// adopt makes the workspace the controller of an imported claim. A claim that another controller owns stays as it is.
func (r *workspaceReconciler) adopt(ctx context.Context, ws *v1.IDEWorkspace, pvc *corev1.PersistentVolumeClaim) error {
	if c := metav1.GetControllerOf(pvc); c != nil {
		return nil
	}
	orig := pvc.DeepCopy()
	if err := controllerutil.SetControllerReference(ws, pvc, r.scheme); err != nil {
		return err
	}
	if err := r.Patch(ctx, pvc, client.MergeFrom(orig), client.FieldOwner(fieldManager)); err != nil {
		return client.IgnoreNotFound(err)
	}
	slog.InfoContext(ctx, "adopted the imported claim", "workspace", ws.Name)
	return nil
}

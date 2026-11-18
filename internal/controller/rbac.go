// Package controller holds the operator's reconcilers. The markers below are the
// operator's permissions; make generate writes them to config/rbac and the chart
// turns them into a namespaced Role.
package controller

// +kubebuilder:rbac:groups=eduide.github.io,resources=idetemplates;idepools;idesessions;ideworkspaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=eduide.github.io,resources=idetemplates/status;idepools/status;idesessions/status;ideworkspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=eduide.github.io,resources=idesessions/finalizers;ideworkspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=eduide.github.io,resources=ideworkspaces,verbs=delete
// +kubebuilder:rbac:groups="",resources=pods;services;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

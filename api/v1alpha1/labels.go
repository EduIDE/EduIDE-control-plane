package v1alpha1

// Labels and annotations shared by all components. They are a contract:
// the operator writes them, service, authz, pool-scaler and the charts read them.
const (
	Prefix = "eduide.github.io/"

	// LabelManagedBy marks every object the control plane creates; informers filter on it.
	LabelManagedBy      = "app.kubernetes.io/managed-by"
	LabelManagedByValue = "eduide"

	LabelComponent     = Prefix + "component" // session (IDE pod), workspace (PVC)
	ComponentSession   = "session"
	ComponentWorkspace = "workspace"
	LabelTemplate      = Prefix + "template"
	LabelPool          = Prefix + "pool"
	LabelSession       = Prefix + "session"
	LabelWorkspace     = Prefix + "workspace"
	LabelInstance      = Prefix + "instance"   // random, unguessable instance id; also the URL path segment
	LabelState         = Prefix + "state"      // StateIdle or StateClaimed on session pods
	LabelOwnerHash     = Prefix + "owner-hash" // OwnerHash(owner), owner names are not label-safe
	LabelLaunchKey     = Prefix + "launch-key" // idempotency key of a launch (G14)
	LabelRoute         = Prefix + "route"      // RouteSession on session HTTPRoutes, selected by the SecurityPolicy
	LabelNodePool      = Prefix + "node-pool"  // value of the configured node pool label of the pod's placement
	StateIdle          = "idle"
	StateClaimed       = "claimed"
	RouteSession       = "session"

	AnnotationTraceparent = Prefix + "traceparent" // W3C trace context of the launch
	AnnotationSpecHash    = Prefix + "spec-hash"   // hash of template spec and agent image a pool pod was built from
	AnnotationRetain      = Prefix + "retain"      // "true" on an IDEWorkspace cancels expiry (G19)

	// Pool scaler annotations on IDEPool (G6).
	AnnotationScalerMode     = Prefix + "scaler-mode"
	AnnotationScalerOverride = Prefix + "scaler-override"

	// Instance token: header set by the HTTPRoute, checked by the agent (G4).
	InstanceTokenHeader = "X-EduIDE-Instance-Token" // #nosec G101 -- a header name, not a credential
	InstanceTokenKey    = "token"

	// Finalizer on IDESession so the operator can record workspace usage at the end (G19).
	SessionFinalizer = Prefix + "session"
)

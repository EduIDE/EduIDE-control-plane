package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// IDESessionSpec is one running IDE. The whole spec is immutable.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type IDESessionSpec struct {
	// Owner is set by the service from the verified token, never from the request.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=320
	Owner string `json:"owner"`
	// +kubebuilder:validation:MinLength=1
	TemplateRef string `json:"templateRef"`
	// WorkspaceRef names an IDEWorkspace; empty means no persistence.
	// +optional
	WorkspaceRef string `json:"workspaceRef,omitempty"`
	// DataSecretRef names the Secret with the launch data (G18).
	// +optional
	DataSecretRef string `json:"dataSecretRef,omitempty"`
	// +optional
	Display Display `json:"display,omitempty"`
}

// +kubebuilder:validation:Enum=Pending;Starting;Ready;Failed
type SessionPhase string

const (
	SessionPending  SessionPhase = "Pending"
	SessionStarting SessionPhase = "Starting"
	SessionReady    SessionPhase = "Ready"
	SessionFailed   SessionPhase = "Failed"
)

// +kubebuilder:validation:Enum=warm-ready;warm-starting;cold
type StartStrategy string

const (
	StartWarmReady    StartStrategy = "warm-ready"
	StartWarmStarting StartStrategy = "warm-starting"
	StartCold         StartStrategy = "cold"
)

// Stable error codes, shared with the REST API.
const (
	ErrorCapacityExhausted = "capacity-exhausted"
	ErrorTemplateNotFound  = "template-not-found"
	ErrorWorkspaceNotFound = "workspace-not-found"
	ErrorStartFailed       = "start-failed"
	ErrorWorkspaceInUse    = "workspace-in-use" // unused since U14, kept for compatibility
)

type SessionTimestamps struct {
	// +optional
	Claimed *metav1.Time `json:"claimed,omitempty"`
	// +optional
	PodReady *metav1.Time `json:"podReady,omitempty"`
	// +optional
	AgentReady *metav1.Time `json:"agentReady,omitempty"`
	// +optional
	Ready *metav1.Time `json:"ready,omitempty"`
}

type IDESessionStatus struct {
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`
	// +optional
	StartStrategy StartStrategy `json:"startStrategy,omitempty"`
	// Instance is the random instance id; it is the first URL path segment.
	// +optional
	Instance string `json:"instance,omitempty"`
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
	// ErrorCode is one of the stable error codes when phase is Failed.
	// +optional
	ErrorCode string `json:"errorCode,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	Timestamps SessionTimestamps `json:"timestamps,omitempty"`
	// LastActivity is coarse (only written when it moves by more than 5 min).
	// +optional
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// IDESession is one running IDE of one user.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ides,categories=eduide
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.status.startStrategy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type IDESession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IDESessionSpec   `json:"spec"`
	Status            IDESessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IDESessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IDESession `json:"items"`
}

func init() { register(&IDESession{}, &IDESessionList{}) }

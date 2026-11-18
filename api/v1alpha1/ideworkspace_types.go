package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspacePersistenceMode is the storage of a workspace. snapshot is added in phase 9.
// +kubebuilder:validation:Enum=volume
type WorkspacePersistenceMode string

const WorkspaceVolume WorkspacePersistenceMode = "volume"

type WorkspacePersistence struct {
	// +kubebuilder:default=volume
	Mode WorkspacePersistenceMode `json:"mode"`
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
	// ExistingClaimName imports an existing PVC instead of creating one (migration).
	// +optional
	ExistingClaimName string `json:"existingClaimName,omitempty"`
}

// IDEWorkspaceSpec describes persistent user files.
type IDEWorkspaceSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="owner is immutable"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=320
	Owner string `json:"owner"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="templateRef is immutable"
	// +kubebuilder:validation:MinLength=1
	TemplateRef string `json:"templateRef"`
	// +kubebuilder:validation:XValidation:rule="self.mode == oldSelf.mode",message="mode is immutable"
	Persistence WorkspacePersistence `json:"persistence"`
	// Retention after last use; overrides template and deployment (G19).
	// +optional
	Retention *metav1.Duration `json:"retention,omitempty"`
	// +optional
	Display Display `json:"display,omitempty"`
}

// +kubebuilder:validation:Enum=Pending;Ready;Expired
type WorkspacePhase string

const (
	WorkspacePending WorkspacePhase = "Pending"
	WorkspaceReady   WorkspacePhase = "Ready"
	WorkspaceExpired WorkspacePhase = "Expired"
)

type IDEWorkspaceStatus struct {
	// +optional
	Phase WorkspacePhase `json:"phase,omitempty"`
	// +optional
	ClaimName string `json:"claimName,omitempty"`
	// LastUsed is set at start and end of every session and on import.
	// +optional
	LastUsed *metav1.Time `json:"lastUsed,omitempty"`
	// ExpiresAt is lastUsed plus retention.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// ExpiredAt marks the start of the 24 h grace period before deletion.
	// +optional
	ExpiredAt *metav1.Time `json:"expiredAt,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// IDEWorkspace holds persistent user files.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=idew,categories=eduide
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Last used",type=date,JSONPath=`.status.lastUsed`
type IDEWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IDEWorkspaceSpec   `json:"spec"`
	Status            IDEWorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IDEWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IDEWorkspace `json:"items"`
}

func init() { SchemeBuilder.Register(&IDEWorkspace{}, &IDEWorkspaceList{}) }

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// IDEPoolSpec keeps prewarmed instances of one template.
// The operator keeps clamp(max(minIdle, targetIdle), 0, maxInstances-claimed) idle
// instances, and 0 while its node pool is under capacity pressure (G8).
// +kubebuilder:validation:XValidation:rule="self.minIdle <= self.maxInstances",message="minIdle must not exceed maxInstances"
// +kubebuilder:validation:XValidation:rule="self.targetIdle <= self.maxInstances",message="targetIdle must not exceed maxInstances"
type IDEPoolSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="templateRef is immutable"
	// +kubebuilder:validation:MinLength=1
	TemplateRef string `json:"templateRef"`
	// MinIdle is the floor set by Helm.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	MinIdle int32 `json:"minIdle"`
	// TargetIdle is set by the pool scaler, also via /scale.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	TargetIdle int32 `json:"targetIdle"`
	// MaxInstances caps idle plus claimed instances of this pool.
	// +kubebuilder:validation:Minimum=0
	MaxInstances int32 `json:"maxInstances"`
}

type IDEPoolStatus struct {
	// Replicas is idleReady plus idleStarting; the /scale status.
	Replicas     int32 `json:"replicas"`
	IdleReady    int32 `json:"idleReady"`
	IdleStarting int32 `json:"idleStarting"`
	Claimed      int32 `json:"claimed"`
	// Selector of idle pods, for /scale.
	// +optional
	Selector string `json:"selector,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// IDEPool prewarms instances of one IDETemplate.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.targetIdle,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=idep,categories=eduide
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef`
// +kubebuilder:printcolumn:name="Target",type=integer,JSONPath=`.spec.targetIdle`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.idleReady`
// +kubebuilder:printcolumn:name="Starting",type=integer,JSONPath=`.status.idleStarting`
// +kubebuilder:printcolumn:name="Claimed",type=integer,JSONPath=`.status.claimed`
type IDEPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IDEPoolSpec   `json:"spec"`
	Status            IDEPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IDEPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IDEPool `json:"items"`
}

func init() { register(&IDEPool{}, &IDEPoolList{}) }

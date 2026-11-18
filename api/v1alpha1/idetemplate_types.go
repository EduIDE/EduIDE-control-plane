package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeyFormat is the check applied to an injectable value (G18).
// +kubebuilder:validation:Enum=HTTPSURL;Token;Enum;Pattern
type KeyFormat string

const (
	// FormatHTTPSURL accepts only https URLs whose host is on the deployment allowlist.
	FormatHTTPSURL KeyFormat = "HTTPSURL"
	// FormatToken accepts printable ASCII without whitespace up to maxLength.
	FormatToken KeyFormat = "Token"
	// FormatEnum accepts one of values.
	FormatEnum KeyFormat = "Enum"
	// FormatPattern accepts values matching pattern (RE2, anchored).
	FormatPattern KeyFormat = "Pattern"
)

// KeySource says who provides the value.
// +kubebuilder:validation:Enum=Request;UserName;UserEmail
type KeySource string

const (
	SourceRequest   KeySource = "Request"   // from the launch request, checked by format
	SourceUserName  KeySource = "UserName"  // set by the service from the token
	SourceUserEmail KeySource = "UserEmail" // set by the service from the token
)

// InjectableKey allows one key of launch data to reach the session.
// +kubebuilder:validation:XValidation:rule="self.source != 'Request' || has(self.format)",message="format is required for keys from the request"
// +kubebuilder:validation:XValidation:rule="!has(self.format) || self.format != 'Enum' || size(self.values) > 0",message="Enum needs values"
// +kubebuilder:validation:XValidation:rule="!has(self.format) || self.format != 'Pattern' || size(self.pattern) > 0",message="Pattern needs pattern"
type InjectableKey struct {
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Z0-9_]{0,63}$`
	Name string `json:"name"`
	// +kubebuilder:default=Request
	// +optional
	Source KeySource `json:"source,omitempty"`
	// +optional
	Format KeyFormat `json:"format,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8192
	// +kubebuilder:default=2048
	// +optional
	MaxLength int32 `json:"maxLength,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	// +optional
	Values []string `json:"values,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Pattern string `json:"pattern,omitempty"`
	// +optional
	Required bool `json:"required,omitempty"`
}

// EnvVar is a static environment variable. There is deliberately no valueFrom (G18).
type EnvVar struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
}

// TemplatePersistence lists the persistence modes a template allows.
// +kubebuilder:validation:XValidation:rule="self.default in self.allowed",message="default must be one of allowed"
type TemplatePersistence struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=set
	Allowed []PersistenceMode `json:"allowed"`
	// +kubebuilder:default=none
	Default PersistenceMode `json:"default"`
	// Size of new workspace volumes; the deployment default applies when empty.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
	// MountPath of the workspace in the IDE container.
	// +kubebuilder:default=/home/project
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// Timeouts of a session (G14).
// +kubebuilder:validation:XValidation:rule="!has(self.warning) || !has(self.idle) || duration(self.warning) < duration(self.idle)",message="warning must be shorter than idle"
// +kubebuilder:validation:XValidation:rule="!has(self.max) || !has(self.idle) || duration(self.max) >= duration(self.idle)",message="max must not be shorter than idle"
type Timeouts struct {
	// Idle ends a session without activity. Default 60m.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	Idle *metav1.Duration `json:"idle,omitempty"`
	// Warning tells the user this long after the last activity. Default 50m.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	Warning *metav1.Duration `json:"warning,omitempty"`
	// Max ends a session regardless of activity. Default 24h.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	Max *metav1.Duration `json:"max,omitempty"`
}

// Placement of session pods (U7). Empty fields fall back to the deployment profile.
type Placement struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// Bandwidth limits per session pod (kubernetes.io/{ingress,egress}-bandwidth).
type Bandwidth struct {
	// +optional
	Ingress *resource.Quantity `json:"ingress,omitempty"`
	// +optional
	Egress *resource.Quantity `json:"egress,omitempty"`
}

// IDETemplateSpec describes one kind of IDE.
type IDETemplateSpec struct {
	// +kubebuilder:validation:MaxLength=200
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	LogoURL string `json:"logoURL,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// Port the IDE listens on, on 127.0.0.1 inside the pod (G4).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=3000
	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	// +optional
	Env []EnvVar `json:"env,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	// +optional
	InjectableKeys []InjectableKey     `json:"injectableKeys,omitempty"`
	Persistence    TemplatePersistence `json:"persistence"`
	// +optional
	Timeouts Timeouts `json:"timeouts,omitempty"`
	// Retention of workspaces after last use; overrides the deployment default (G19).
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	Retention *metav1.Duration `json:"retention,omitempty"`
	// +optional
	Placement Placement `json:"placement,omitempty"`
	// +optional
	Bandwidth *Bandwidth `json:"bandwidth,omitempty"`
	// RunAsUser and FSGroup of the IDE container.
	// +optional
	RunAsUser *int64 `json:"runAsUser,omitempty"`
}

// ImageVariant is a project template of the IDE image (U12), chosen at launch as TEMPLATE.
type ImageVariant struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._-]{0,63}$`
	ID string `json:"id"`
	// +kubebuilder:validation:MaxLength=200
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	LogoURL string `json:"logoURL,omitempty"`
}

// IncludedItem is one tool of the image with the version installed at build time.
type IncludedItem struct {
	// +kubebuilder:validation:MaxLength=200
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Version string `json:"version,omitempty"`
}

// IncludedGroup groups what the image brings along, e.g. languages or build tools.
type IncludedGroup struct {
	// +kubebuilder:validation:MaxLength=100
	Group string `json:"group"`
	// +kubebuilder:validation:MaxItems=256
	Items []IncludedItem `json:"items"`
}

// ImageMetadata is read from the OCI annotation io.eduide.template of the image manifest (U12).
type ImageMetadata struct {
	// Digest of the manifest the metadata was read from.
	// +optional
	Digest string `json:"digest,omitempty"`
	// Version of the EduIDE build in the image.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Version string `json:"version,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	Variants []ImageVariant `json:"variants,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	// +optional
	Includes []IncludedGroup `json:"includes,omitempty"`
	// LastChecked is when the operator last wrote this block, not when it last asked the registry.
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
}

type IDETemplateStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Image is the metadata of the template image; empty when the image carries none.
	// +optional
	Image *ImageMetadata `json:"image,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// IDETemplate is what kind of IDE a session runs.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=idet,categories=eduide
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type IDETemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IDETemplateSpec   `json:"spec"`
	Status            IDETemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IDETemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IDETemplate `json:"items"`
}

func init() { register(&IDETemplate{}, &IDETemplateList{}) }

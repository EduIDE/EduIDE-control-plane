package v1alpha1

// PersistenceMode selects where user files live. snapshot is reserved for phase 9.
// +kubebuilder:validation:Enum=none;volume
type PersistenceMode string

const (
	PersistenceNone   PersistenceMode = "none"
	PersistenceVolume PersistenceMode = "volume"
)

// Display holds non-secret data the landing page shows (G15).
type Display struct {
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Title string `json:"title,omitempty"`
	// Source is where the content comes from, e.g. the Git URL without credentials.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	Source string `json:"source,omitempty"`
}

// Standard condition types.
const (
	ConditionReady            = "Ready"
	ConditionCapacityPressure = "CapacityPressure"
)

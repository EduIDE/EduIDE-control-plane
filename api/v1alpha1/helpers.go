package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Default session timeouts (G14), used when a template sets none.
const (
	DefaultIdleTimeout    = time.Hour
	DefaultWarningTimeout = 50 * time.Minute
	DefaultMaxTimeout     = 24 * time.Hour
)

// AnnotationImageMetadata is the OCI annotation of the template image manifest that holds the template metadata (U12).
const AnnotationImageMetadata = "io.eduide.template"

// OwnerHash is the label-safe form of an owner name.
func OwnerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])[:40]
}

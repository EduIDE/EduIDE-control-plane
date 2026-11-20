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

// ResolvedTimeouts are the session timeouts every component applies to a template: the operator ends sessions
// by them, the service shows them. The CRD rules compare only the fields a template sets, so one field can
// contradict the default of another; this is the one place that makes them consistent.
//   - A missing, zero or negative idle or max timeout is the default (zero would end a session when it is ready).
//   - The warning comes before the idle end; otherwise it keeps the ratio of the defaults.
func (t *IDETemplate) ResolvedTimeouts() (idle, warning, max time.Duration) {
	idle, warning, max = DefaultIdleTimeout, DefaultWarningTimeout, DefaultMaxTimeout
	if t != nil {
		if d := t.Spec.Timeouts.Idle; d != nil && d.Duration > 0 {
			idle = d.Duration
		}
		if d := t.Spec.Timeouts.Warning; d != nil {
			warning = d.Duration
		}
		if d := t.Spec.Timeouts.Max; d != nil && d.Duration > 0 {
			max = d.Duration
		}
	}
	if warning <= 0 || warning >= idle {
		warning = time.Duration(float64(idle) * float64(DefaultWarningTimeout) / float64(DefaultIdleTimeout))
	}
	return idle, warning, max
}

// AnnotationImageMetadata is the OCI annotation of the template image manifest that holds the template metadata (U12).
const AnnotationImageMetadata = "io.eduide.template"

// OwnerHash is the label-safe form of an owner name.
func OwnerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])[:40]
}

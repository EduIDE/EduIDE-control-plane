package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
)

// OwnerHash is the label-safe form of an owner name.
func OwnerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])[:40]
}

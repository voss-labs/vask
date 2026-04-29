// Package auth turns an SSH public key into the irreversible identity hash
// used everywhere else in the codebase.
//
// We never store the raw public key. We store sha256(marshalled_pubkey) only.
// That hash is the single column linking a connection to a row in users.
package auth

import (
	"crypto/sha256"
	"encoding/hex"

	gossh "golang.org/x/crypto/ssh"
)

// Fingerprint returns the lowercase hex sha256 of a public key's wire format.
// Two connections from the same key will always produce the same string.
// The original key cannot be recovered from this value.
func Fingerprint(key gossh.PublicKey) string {
	if key == nil {
		return ""
	}
	sum := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(sum[:])
}

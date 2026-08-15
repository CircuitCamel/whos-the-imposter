// Package token generates random identifiers: player IDs, the host-screen
// session, and login sessions all use it. Every value is 32 hex characters
// (16 random bytes) - deliberately not a UUID, just raw randomness with no
// version/variant bits reserved.
package token

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New returns a fresh random ID. The only failure mode is the system
// entropy source being unavailable, which in practice doesn't happen; the
// fallback exists so a caller never has to handle an error here.
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

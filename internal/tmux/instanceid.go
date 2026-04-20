package tmux

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateInstanceID returns a 12-character hex string (48 bits of entropy)
// suitable for uniquely identifying a window+sidebar+terminals combo.
// Collision-free in practice for ~30 concurrent windows.
func GenerateInstanceID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

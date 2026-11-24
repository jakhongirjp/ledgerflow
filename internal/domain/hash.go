package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RequestHash fingerprints the client-supplied part of a transaction so a
// replayed idempotency key can be checked against the original request.
func RequestHash(description string, entries []Entry) string {
	payload := struct {
		D string  `json:"d"`
		E []Entry `json:"e"`
	}{description, entries}
	b, _ := json.Marshal(payload) // cannot fail for these types
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

package auth

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

const keyPrefix = "sk-"

// GenerateAPIKey returns (raw secret, hash). The raw secret is shown once.
func GenerateAPIKey() (string, string) {
	raw := keyPrefix + uuid.NewString() + uuid.NewString()
	return raw, HashAPIKey(raw)
}

// HashAPIKey returns the hex sha256 of a raw API key. Only the hash is stored.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

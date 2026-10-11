package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// RefreshTTL is how long a refresh token is valid. Each use replaces it.
const RefreshTTL = 30 * 24 * time.Hour

// NewRefreshToken returns a random token for the client and the hash to store.
func NewRefreshToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashRefreshToken(plain), nil
}

// HashRefreshToken is SHA-256 hex. Tokens are high-entropy random values, so
// a fast hash is enough; the database never sees the plain token.
func HashRefreshToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

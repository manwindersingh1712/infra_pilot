// Package secrets encrypts env var values at rest with AES-256-GCM.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const prefix = "enc:v1:"

// Box encrypts and decrypts values with one key.
type Box struct{ aead cipher.AEAD }

// New takes the 32-byte key from ENV_ENCRYPTION_KEY.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns "enc:v1:" + base64(nonce || ciphertext).
func (b *Box) Encrypt(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Values without the prefix are legacy plain text
// and are returned unchanged.
func (b *Box) Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", errors.New("secrets: malformed encrypted value")
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("secrets: malformed encrypted value")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("secrets: cannot decrypt value (wrong key or tampered)")
	}
	return string(plain), nil
}

// IsEncrypted reports whether value carries the "enc:v1:" prefix.
func IsEncrypted(value string) bool { return strings.HasPrefix(value, prefix) }

// RandomPassword returns 32 URL-safe characters (24 random bytes), safe to put
// in connection strings and shell-quoted commands.
func RandomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

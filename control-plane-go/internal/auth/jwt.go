// Package auth issues and checks access tokens, refresh tokens and password
// hashes. Password hashes are compatible with the Node API's bcrypt hashes.
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessTTL is how long an access token is valid; the UI refreshes it.
const AccessTTL = 15 * time.Minute

type Signer struct {
	secret []byte
	now    func() time.Time
}

// NewSigner returns an error for an empty secret. The length policy for real
// deployments lives in config; this is only a guard against an unset secret.
func NewSigner(secret string) (*Signer, error) {
	if secret == "" {
		return nil, errors.New("auth: empty JWT secret")
	}
	return &Signer{secret: []byte(secret), now: time.Now}, nil
}

// Sign returns an HS256 token with claims {sub, iat, exp}.
func (s *Signer) Sign(userID string) (string, error) {
	now := s.now()
	claims := jwt.MapClaims{"sub": userID, "iat": now.Unix(), "exp": now.Add(AccessTTL).Unix()}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// Verify checks signature and expiry (exp is required) and returns sub.
func (s *Signer) Verify(token string) (string, error) {
	if token == "" {
		return "", errors.New("missing token")
	}
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.now))
	if err != nil {
		return "", err
	}
	sub, err := parsed.Claims.GetSubject()
	if err != nil || sub == "" {
		return "", errors.New("token has no sub claim")
	}
	return sub, nil
}
